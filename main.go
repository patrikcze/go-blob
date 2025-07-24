package main

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	_ "github.com/Azure/azure-sdk-for-go/sdk/azcore/streaming"
	_ "github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/sas"
	"golang.org/x/time/rate"
)

// Define a struct to hold the template data
type TemplateData struct {
	Progress int
}

// UploadState represents the current state of an upload operation
type UploadState struct {
	UploadedBytes int64
	Percentage    float64
	FileSize      int64
	mu            sync.Mutex
}

// progressReader wraps an io.Reader to track bytes read and report progress
type progressReader struct {
	r      io.Reader
	total  int64
	update func(int64)
	read   int64
	closed bool
	mu     sync.Mutex
}

// Read implements io.Reader interface and tracks progress
func (pr *progressReader) Read(p []byte) (int, error) {
	n, err := pr.r.Read(p)
	if n > 0 {
		pr.read += int64(n)
		pr.update(pr.read)
	}
	return n, err
}

// Seek implements io.Seeker interface by delegating to underlying reader if it supports seeking
func (pr *progressReader) Seek(offset int64, whence int) (int64, error) {
	if seeker, ok := pr.r.(io.Seeker); ok {
		pos, err := seeker.Seek(offset, whence)
		if err == nil {
			// Reset read counter based on seek position
			pr.read = pos
			pr.update(pr.read)
		}
		return pos, err
	}
	return 0, fmt.Errorf("underlying reader does not support seeking")
}

// Close implements io.Closer interface by delegating to underlying reader if it supports closing
func (pr *progressReader) Close() error {
	pr.mu.Lock()
	defer pr.mu.Unlock()
	
	// Prevent double-close
	if pr.closed {
		return nil
	}
	pr.closed = true
	
	if closer, ok := pr.r.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}

// Update updates the state of the upload with thread safety
func (s *UploadState) Update(bytesTransferred int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.UploadedBytes = bytesTransferred
	if s.FileSize > 0 {
		s.Percentage = (float64(bytesTransferred) / float64(s.FileSize)) * 100
	}
	
	// Broadcast progress only to clients connected with the current upload session
	broadcastProgressToSession(currentUploadSession, ProgressUpdate{
		Percentage:    s.Percentage,
		BytesUploaded: bytesTransferred,
		TotalBytes:    s.FileSize,
		Status:        "uploading",
		Message:       fmt.Sprintf("Uploaded %s of %s", formatBytes(bytesTransferred), formatBytes(s.FileSize)),
	})
}

// GetPercentage returns the current percentage with thread safety
func (s *UploadState) GetPercentage() float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Percentage
}

// AppConfig holds application configuration
type AppConfig struct {
	StorageAccountName string
	StorageAccountKey  string
	StorageContainer   string
	TempDir            string
	AllowedFileTypes   map[string]bool
	MinFileSize        int64
	MaxFileSize        int64
	DevMode            bool // true for local development without Azure
}

// Global variables
var (
	appConfig        AppConfig
	uploadState      = UploadState{}
	uploadLimiter    *rate.Limiter
	uploadMutex      sync.Mutex // Mutex for controlling concurrent uploads
	uploadInProgress bool
	// SSE channels for real-time progress updates - organized by session ID
	progressClients = make(map[string]map[chan ProgressUpdate]bool)
	progressMutex   sync.RWMutex
	// Current upload session
	currentUploadSession string
)

// ProgressUpdate represents a progress update message
type ProgressUpdate struct {
	Percentage   float64 `json:"percentage"`
	BytesUploaded int64   `json:"bytesUploaded"`
	TotalBytes   int64   `json:"totalBytes"`
	Speed        string  `json:"speed"`
	ETA          string  `json:"eta"`
	Status       string  `json:"status"`
	Message      string  `json:"message"`
}

const (
	// Set max file size to support up to 4 GB
	maxFileSize = 4 * 1024 * 1024 * 1024

	// Set multipart form memory to a reasonable size (32MB)
	maxFormMemory = 32 * 1024 * 1024

	// MinFileSize indicates the minimum allowed file size (1KB)
	minFileSize = 1 * 1024

	// BlockBlobMaxUploadBlobBytes indicates the maximum number of bytes that can be sent in a call to Upload.
	BlockBlobMaxUploadBlobBytes = 256 * 1024 * 1024 // 256MB

	// BlockBlobMaxStageBlockBytes indicates the maximum number of bytes that can be sent in a call to StageBlock.
	BlockBlobMaxStageBlockBytes = 4000 * 1024 * 1024 // 4000MiB

	// BlockBlobMaxBlocks indicates the maximum number of blocks allowed in a block blob.
	BlockBlobMaxBlocks = 50000

	// SASTimeFormat is the time format for SAS tokens
	SASTimeFormat = "2006-01-02T15:04:05Z"

	// RateLimitRequestsPerSecond defines the maximum number of upload requests allowed per second
	RateLimitRequestsPerSecond = 5

	// RateLimitBurst defines the maximum burst size for the rate limiter
	RateLimitBurst = 10
)

// Initialize the Azure Blob Storage credentials and app configuration
func init() {
	// Check for dev mode environment variable
	devMode := os.Getenv("DEV_MODE") == "true"

	// Initialize the rate limiter
	uploadLimiter = rate.NewLimiter(rate.Limit(RateLimitRequestsPerSecond), RateLimitBurst)

	// Create allowed file types map
	allowedTypes := map[string]bool{
		".jpg":  true,
		".jpeg": true,
		".png":  true,
		".gif":  true,
		".pdf":  true,
		".doc":  true,
		".docx": true,
		".xls":  true,
		".xlsx": true,
		".txt":  true,
		".csv":  true,
		".zip":  true,
		".iso":  true,
		".mp4":  true,
		".tgz":  true,
		".tar":  true,
		".gz":   true,
		".7z":   true,
	}

	// Set up application configuration
	appConfig = AppConfig{
		TempDir:          "temp",
		AllowedFileTypes: allowedTypes,
		MinFileSize:      minFileSize,
		MaxFileSize:      maxFileSize,
		DevMode:          devMode,
	}
	// Only check Azure credentials if not in dev mode
	if !appConfig.DevMode {
		// Azure Storage Account Name
		storageAccountNameEnv, exists := os.LookupEnv("AZURE_STORAGE_ACCOUNT_NAME")
		if !exists || storageAccountNameEnv == "" {
			log.Fatal("AZURE_STORAGE_ACCOUNT_NAME is not set. Run with DEV_MODE=true to use local storage only.")
		} else {
			appConfig.StorageAccountName = storageAccountNameEnv
		}

		// Azure Storage Account Key
		storageAccountKeyEnv, exists := os.LookupEnv("AZURE_STORAGE_ACCOUNT_KEY")
		if !exists || storageAccountKeyEnv == "" {
			log.Fatal("AZURE_STORAGE_ACCOUNT_KEY is not set. Run with DEV_MODE=true to use local storage only.")
		} else {
			appConfig.StorageAccountKey = storageAccountKeyEnv
		}

		// Azure Storage Account Container
		storageContainerEnv, exists := os.LookupEnv("AZURE_STORAGE_ACCOUNT_CONTAINER")
		if !exists || storageContainerEnv == "" {
			// Use a default container name
			appConfig.StorageContainer = "upload"
		} else {
			appConfig.StorageContainer = storageContainerEnv
		}
	} else {
		log.Println("Running in development mode - Azure storage is disabled")
		appConfig.StorageContainer = "local"
	}

	// Ensure temp directory exists
	if err := ensureTempDirExists(); err != nil {
		log.Fatalf("Failed to create temp directory: %v", err)
	}

	// Clean up any lingering temporary files
	cleanupTempFiles()
}

// ensureTempDirExists creates the temporary directory if it doesn't exist
func ensureTempDirExists() error {
	_, err := os.Stat(appConfig.TempDir)
	if os.IsNotExist(err) {
		return os.MkdirAll(appConfig.TempDir, 0755)
	}
	return err
}

// cleanupTempFiles removes any temporary files that might have been left over
func cleanupTempFiles() {
	files, err := os.ReadDir(appConfig.TempDir)
	if err != nil {
		log.Printf("Warning: Failed to read temp directory during cleanup: %v", err)
		return
	}

	for _, file := range files {
		if !file.IsDir() {
			filePath := filepath.Join(appConfig.TempDir, file.Name())
			if err := os.Remove(filePath); err != nil {
				log.Printf("Warning: Failed to remove temporary file %s: %v", filePath, err)
			} else {
				log.Printf("Cleaned up temporary file: %s", filePath)
			}
		}
	}
}
func main() {
	// Set up proper logging
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	if appConfig.DevMode {
		log.Printf("Application starting in DEVELOPMENT MODE with local storage in %s", appConfig.TempDir)
	} else {
		log.Printf("Application starting with storage account: %s, container: %s",
			appConfig.StorageAccountName, appConfig.StorageContainer)
	}

	// Create a file server
	http.HandleFunc("/", handleGet)
	http.HandleFunc("/upload", handlePost)
	http.HandleFunc("/progress", progressHandler)
	http.HandleFunc("/progress-stream", progressStreamHandler)

	// Start the server
	fmt.Println("Starting server on port 9000...")
	server := &http.Server{
		Addr:         ":9000",
		ReadTimeout:  5 * time.Minute,  // Increased for large file uploads
		WriteTimeout: 30 * time.Minute, // Increased for large file uploads
		IdleTimeout:  2 * time.Minute,
	}

	if err := server.ListenAndServe(); err != nil {
		log.Fatalf("Failed to start the server: %v", err)
	}
}

// handleError is a helper function for consistent error handling
func handleError(w http.ResponseWriter, msg string, err error, statusCode int) {
	logMsg := msg
	if err != nil {
		logMsg = fmt.Sprintf("%s: %v", msg, err)
	}

	log.Printf("Error: %s", logMsg)
	http.Error(w, msg, statusCode)
}

// sanitizeFilename removes potentially dangerous characters from filename
func sanitizeFilename(filename string) string {
	// Remove path separators and other dangerous characters
	unsafeChars := regexp.MustCompile(`[<>:"/\\|?*\x00-\x1f]`)
	sanitized := unsafeChars.ReplaceAllString(filename, "_")
	
	// Use only the base filename (no directory traversal)
	sanitized = filepath.Base(sanitized)
	
	// Ensure filename is not empty after sanitization
	if sanitized == "" || sanitized == "." || sanitized == ".." {
		sanitized = "uploaded_file"
	}
	
	// Limit filename length
	if len(sanitized) > 200 {
		ext := filepath.Ext(sanitized)
		name := sanitized[:200-len(ext)]
		sanitized = name + ext
	}
	
	return sanitized
}

// validateFileType checks if the provided file extension is allowed
// Returns a bool indicating if the type is allowed and the extension string
func validateFileType(filename string) (bool, string) {
	ext := strings.ToLower(filepath.Ext(filename))
	allowed := appConfig.AllowedFileTypes[ext]
	log.Printf("File type validation: %s is %v", ext, allowed)
	return allowed, ext
}

// validateMimeType checks if the file content matches expected MIME type
func validateMimeType(file io.ReadSeeker, expectedExt string) (bool, error) {
	// Read first 512 bytes for MIME detection
	buffer := make([]byte, 512)
	n, err := file.Read(buffer)
	if err != nil && err != io.EOF {
		return false, err
	}
	
	// Reset file position
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return false, err
	}
	
	detectedType := http.DetectContentType(buffer[:n])
	
	// Define expected MIME types for extensions
	expectedMimes := map[string][]string{
		".jpg":  {"image/jpeg"},
		".jpeg": {"image/jpeg"},
		".png":  {"image/png"},
		".gif":  {"image/gif"},
		".pdf":  {"application/pdf"},
		".txt":  {"text/plain"},
		".csv":  {"text/csv", "text/plain"},
		".zip":  {"application/zip"},
		".gz":   {"application/gzip", "application/x-gzip"},
		".tar":  {"application/x-tar"},
		".7z":   {"application/x-7z-compressed"},
		// Office documents might be detected as application/octet-stream
		".doc":  {"application/msword", "application/octet-stream"},
		".docx": {"application/vnd.openxmlformats-officedocument.wordprocessingml.document", "application/octet-stream"},
		".xls":  {"application/vnd.ms-excel", "application/octet-stream"},
		".xlsx": {"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", "application/octet-stream"},
		".mp4":  {"video/mp4"},
		".iso":  {"application/octet-stream"}, // ISO files are typically binary
		".tgz":  {"application/gzip", "application/x-gzip"},
	}
	
	mimeTypes, exists := expectedMimes[expectedExt]
	if !exists {
		// If we don't have MIME validation for this extension, allow it
		return true, nil
	}
	
	for _, mimeType := range mimeTypes {
		if detectedType == mimeType {
			return true, nil
		}
	}
	
	log.Printf("MIME type mismatch: detected %s for extension %s", detectedType, expectedExt)
	return false, nil
}

// validateFileSize checks if the file size is within the allowed limits
func validateFileSize(size int64) bool {
	return size >= appConfig.MinFileSize && size <= appConfig.MaxFileSize
}

// safeClose attempts to close a file and logs any errors
func safeClose(f io.Closer, name string) error {
	if f == nil {
		return nil
	}
	if err := f.Close(); err != nil {
		log.Printf("Error closing %s: %v", name, err)
		return err
	}
	return nil
}

// cleanupTempFile removes a temporary file and logs any errors
func cleanupTempFile(path string) {
	if path == "" {
		return
	}
	if err := os.Remove(path); err != nil {
		log.Printf("Error removing temporary file %s: %v", path, err)
	} else {
		log.Printf("Removed temporary file: %s", path)
	}
}

// handleGet serves the upload form page
func handleGet(w http.ResponseWriter, r *http.Request) {
	log.Printf("Handling GET request from %s", r.RemoteAddr)
	if r.Method != http.MethodGet {
		handleError(w, "Method not allowed", nil, http.StatusMethodNotAllowed)
		return
	}

	// Parse the HTML template
	log.Print("Parsing HTML template...")
	tmpl, err := template.ParseFiles("index.html")
	if err != nil {
		handleError(w, "Error parsing HTML template", err, http.StatusInternalServerError)
		return
	}

	// Initialize template data with empty progress
	log.Print("Initializing template data...")
	templateData := TemplateData{
		Progress: 0,
	}

	// Execute the template
	log.Print("Executing template...")
	err = tmpl.Execute(w, templateData)
	if err != nil {
		handleError(w, "Error executing template", err, http.StatusInternalServerError)
		return
	}

	log.Print("GET request successfully handled")
}

func handlePost(w http.ResponseWriter, r *http.Request) {
	// Check if the method is POST
	if r.Method != http.MethodPost {
		handleError(w, "Method not allowed", nil, http.StatusMethodNotAllowed)
		return
	}
	
	// Get session ID from form data or query parameters
	sessionID := r.FormValue("sessionID")
	if sessionID == "" {
		sessionID = r.URL.Query().Get("sessionID")
	}
	if sessionID == "" {
		handleError(w, "Session ID required", nil, http.StatusBadRequest)
		return
	}
	
	// Set the current upload session
	currentUploadSession = sessionID

	// Apply rate limiting
	if !uploadLimiter.Allow() {
		handleError(w, "Too many upload requests, please try again later", nil, http.StatusTooManyRequests)
		return
	}

	// Ensure only one upload happens at a time
	uploadMutex.Lock()
	if uploadInProgress {
		uploadMutex.Unlock()
		handleError(w, "Another upload is in progress, please try again later", nil, http.StatusServiceUnavailable)
		return
	}

	uploadInProgress = true
	uploadMutex.Unlock()

	// Set up a deferred function to reset the upload state
	defer func() {
		uploadMutex.Lock()
		uploadInProgress = false
		uploadMutex.Unlock()
	}()
	// Limit the size of the request body to prevent denial of service attacks
	r.Body = http.MaxBytesReader(w, r.Body, maxFileSize)
	log.Printf("Setting form memory buffer to: %v bytes", maxFormMemory)

	// Parse multipart form with reasonable memory limit
	if err := r.ParseMultipartForm(maxFormMemory); err != nil {
		if err.Error() == "http: request body too large" {
			handleError(w, "File size limit exceeded", err, http.StatusBadRequest)
			return
		}
		handleError(w, "Bad Request", err, http.StatusBadRequest)
		return
	}
	// Process the uploaded files
	for _, fileHeaders := range r.MultipartForm.File {
		for _, fileHeader := range fileHeaders {
			// Sanitize filename first
			sanitizedFilename := sanitizeFilename(fileHeader.Filename)
			log.Printf("Original filename: %s, Sanitized: %s", fileHeader.Filename, sanitizedFilename)
			
			// Validate file type based on sanitized filename
			if valid, ext := validateFileType(sanitizedFilename); !valid {
				handleError(w, fmt.Sprintf("File type '%s' not allowed", ext), nil, http.StatusBadRequest)
				return
			}
			// Validate file size
			if !validateFileSize(fileHeader.Size) {
				handleError(w, fmt.Sprintf("File size must be between %d and %d bytes",
					appConfig.MinFileSize, appConfig.MaxFileSize), nil, http.StatusBadRequest)
				return
			}

			// Open the file
			file, err := fileHeader.Open()
			if err != nil {
				handleError(w, "Error opening file", err, http.StatusInternalServerError)
				return
			}
			
			// Validate MIME type - check if file content matches expected type
			if readSeeker, ok := file.(io.ReadSeeker); ok {
				if valid, err := validateMimeType(readSeeker, strings.ToLower(filepath.Ext(sanitizedFilename))); err != nil {
					handleError(w, "Error validating file content", err, http.StatusInternalServerError)
					return
				} else if !valid {
					handleError(w, "File content does not match expected file type", nil, http.StatusBadRequest)
					return
				}
			} else {
				log.Printf("Warning: Cannot validate MIME type - file does not support seeking")
			}

			// Track whether the file will be closed by progressReader
			var fileClosedByProgressReader bool
			
			// Use a defer with a named function for proper resource cleanup
			defer func() {
				// Only close the file if it wasn't wrapped in a progressReader
				if !fileClosedByProgressReader {
					if err := safeClose(file, "uploaded file"); err != nil {
						log.Printf("Failed to close uploaded file: %v", err)
					}
				}
			}()
			// Get Filename and File size from input file - use sanitized filename
			var fileSize int64 = fileHeader.Size
			var fileName string = sanitizedFilename
			log.Printf("Received file: %s (original: %s), size: %d\n", fileName, fileHeader.Filename, fileSize)

			// Initialize upload state
			uploadState = UploadState{
				UploadedBytes: 0,
				Percentage:    0,
				FileSize:      fileSize,
			}

			// For large files, stream directly to Azure without temporary file buffering
			var tempFile string
			var osFile *os.File
			
			// Only use temp file for dev mode or files smaller than 100MB
				if appConfig.DevMode || fileHeader.Size < 100*1024*1024 {
					// Create temporary file path using sanitized filename
					tempFile = filepath.Join(appConfig.TempDir, sanitizedFilename)

				// Convert multipart.File to os.File
				var err error
				osFile, err = os.Create(tempFile)
				if err != nil {
					handleError(w, "Error creating temporary file", err, http.StatusInternalServerError)
					return
				}
				// Setup defers for cleanup in the right order (last in, first out)
				defer func() {
					// Only clean up temp files if not in dev mode
					if !appConfig.DevMode {
						cleanupTempFile(tempFile)
					} else {
						log.Printf("Dev mode: Keeping temporary file %s", tempFile)
					}
				}()
				defer func() {
					if err := safeClose(osFile, "temp file"); err != nil {
						log.Printf("Failed to close temp file: %v", err)
					}
				}()
				// Stream the file to temporary storage in chunks
				totalSize := fileHeader.Size
				var uploadedSize int64
				buf := make([]byte, 256*1024) // 256KB buffer
				for {
					// Read a chunk
					n, err := file.Read(buf)
					if err != nil && err != io.EOF {
						handleError(w, "Error reading file", err, http.StatusInternalServerError)
						return
					}
					if n == 0 {
						break
					}

					// Write the chunk to the temporary file
					if _, err := osFile.Write(buf[:n]); err != nil {
						handleError(w, "Error writing to temporary file", err, http.StatusInternalServerError)
						return
					}

					// Update the uploaded size
					uploadedSize += int64(n)
					percentage := (float64(uploadedSize) / float64(totalSize)) * 100
					log.Printf("Cached %d bytes of %d (%.2f%%)", uploadedSize, totalSize, percentage)
				}

				// Seek back to beginning of file for upload
				if _, err := osFile.Seek(0, io.SeekStart); err != nil {
					handleError(w, "Error preparing file for upload", err, http.StatusInternalServerError)
					return
				}
			}
			// Check if we're in dev mode or should use Azure
			if !appConfig.DevMode {
				//#######################################
				// Azure SDK
				//#######################################
				// create a Shared Key credential for authenticating with Azure Active Directory
				// Upload to Azure Storage
				credential, err := azblob.NewSharedKeyCredential(appConfig.StorageAccountName, appConfig.StorageAccountKey)
				if err != nil {
					handleError(w, "Error creating Azure Storage credentials", err, http.StatusInternalServerError)
					return
				}

				// Create Azure blob service client
				serviceURL := fmt.Sprintf("https://%s.blob.core.windows.net/", appConfig.StorageAccountName)
				serviceClient, err := azblob.NewClientWithSharedKeyCredential(serviceURL, credential, nil)
				if err != nil {
					handleError(w, "Error creating Azure service client", err, http.StatusInternalServerError)
					return
				}

				// Get container client and then block blob client
				containerClient := serviceClient.ServiceClient().NewContainerClient(appConfig.StorageContainer)
				blockBlobClient := containerClient.NewBlockBlobClient(fileName)

				// Create context with timeout for the upload operation (extended for large files)
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
				defer cancel() // Ensure context resources are released
				
				// Upload with progress meter using resumable upload

				// Upload with progress meter using resumable upload
				var uploadErr error
				if osFile != nil {
					// Upload from temporary file (smaller files or dev mode)
					progressFile := &progressReader{
						r:      osFile,
						total:  fileSize,
						update: uploadState.Update,
					}
					fileClosedByProgressReader = true
					_, uploadErr = blockBlobClient.Upload(ctx, progressFile, nil)
				} else {
					// Stream upload directly from multipart file (large files)
					// Create a progress-tracking reader wrapper
					progressFile := &progressReader{
						r:      file,
						total:  fileSize,
						update: uploadState.Update,
					}
					fileClosedByProgressReader = true
					
					_, uploadErr = blockBlobClient.Upload(ctx, progressFile, nil)
				}
				if uploadErr != nil {
					handleError(w, "Error uploading file to Azure BlockBlob", uploadErr, http.StatusInternalServerError)
					return
				}
				log.Printf("Uploaded %d bytes of %d (%.2f%%)",
					uploadState.UploadedBytes, uploadState.FileSize, uploadState.GetPercentage())
				expiryTime := time.Now().UTC().Add(1 * 24 * time.Hour) // Set Expire time 24 hours
				startTime := time.Now().UTC()

				// Generate SAS URL for uploaded file
				s, err := blockBlobClient.GetSASURL(sas.BlobPermissions{
					Read: true,
				},
					expiryTime,
					&blob.GetSASURLOptions{
						StartTime: &startTime,
					})
				if err != nil {
					handleError(w, "Error generating SAS URL", err, http.StatusInternalServerError)
					return
				}

				// Return success response with SAS URL
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.WriteHeader(http.StatusOK)
				fmt.Fprintf(w, "<h3>File %s uploaded successfully!</h3><br />", fileName)
				fmt.Fprintf(w, "Access your file here: <a href=\"%s\">%s</a><br />", s, fileName)
			} else {
				// In dev mode, just keep the file in temp and return local path
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.WriteHeader(http.StatusOK)
				fmt.Fprintf(w, "<h3>File %s uploaded successfully (Dev Mode)!</h3><br />", fileName)
				if tempFile != "" {
					fmt.Fprintf(w, "File stored at: %s<br />", tempFile)
				} else {
					fmt.Fprintf(w, "File processed (stream mode)<br />")
				}
				// File won't be cleaned up in dev mode due to the defer condition above
			}
		}
	}
} // End of handlePost function

// formatBytes formats bytes into human readable format
func formatBytes(bytes int64) string {
	if bytes == 0 {
		return "0 B"
	}
	const unit = 1024
	sizes := []string{"B", "KB", "MB", "GB", "TB"}
	i := 0
	floatBytes := float64(bytes)
	for floatBytes >= unit && i < len(sizes)-1 {
		floatBytes /= unit
		i++
	}
	return fmt.Sprintf("%.2f %s", floatBytes, sizes[i])
}

// broadcastProgressToSession sends progress updates to SSE clients connected with a specific session ID
func broadcastProgressToSession(sessionID string, update ProgressUpdate) {
	progressMutex.RLock()
	defer progressMutex.RUnlock()
	
	// Get clients for this session
	sessionClients, exists := progressClients[sessionID]
	if !exists {
		return
	}
	
	for client := range sessionClients {
		select {
		case client <- update:
		default:
			// Client channel is full, skip
		}
	}
}

// progressStreamHandler handles Server-Sent Events for real-time progress updates
func progressStreamHandler(w http.ResponseWriter, r *http.Request) {
	// Get session ID from query parameters
	sessionID := r.URL.Query().Get("sessionID")
	if sessionID == "" {
		http.Error(w, "Session ID required", http.StatusBadRequest)
		return
	}
	
	// Set headers for SSE
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	
	// Create a new client channel
	client := make(chan ProgressUpdate, 10)
	
	// Add client to the session's client list
	progressMutex.Lock()
	if progressClients[sessionID] == nil {
		progressClients[sessionID] = make(map[chan ProgressUpdate]bool)
	}
	progressClients[sessionID][client] = true
	progressMutex.Unlock()
	
	// Remove client when done
	defer func() {
		progressMutex.Lock()
		if sessionClients, exists := progressClients[sessionID]; exists {
			delete(sessionClients, client)
			// Clean up empty session maps
			if len(sessionClients) == 0 {
				delete(progressClients, sessionID)
			}
		}
		close(client)
		progressMutex.Unlock()
	}()
	
	// Send initial status
	uploadMutex.Lock()
	isUploading := uploadInProgress
	uploadMutex.Unlock()
	
	if !isUploading {
		initialUpdate := ProgressUpdate{
			Percentage: 0,
			Status:     "ready",
			Message:    "Ready to upload",
		}
		fmt.Fprintf(w, "data: %s\n\n", toJSON(initialUpdate))
		w.(http.Flusher).Flush()
	}
	
	// Create a ticker for heartbeat messages to prevent Safari connection stalling
	heartbeatTicker := time.NewTicker(15 * time.Second)
	defer heartbeatTicker.Stop()
	
	// Listen for progress updates
	for {
		select {
		case update := <-client:
			fmt.Fprintf(w, "data: %s\n\n", toJSON(update))
			w.(http.Flusher).Flush()
		case <-heartbeatTicker.C:
			// Send heartbeat comment to keep connection alive (Safari compatibility)
			fmt.Fprintf(w, ": heartbeat\n\n")
			w.(http.Flusher).Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// toJSON converts struct to JSON string
func toJSON(v interface{}) string {
	data, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(data)
}

// progressHandler handles the progress endpoint
func progressHandler(w http.ResponseWriter, r *http.Request) {
	uploadMutex.Lock()
	isUploading := uploadInProgress
	uploadMutex.Unlock()

	// If no upload is in progress, return 100% (complete)
	if !isUploading {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(struct{ Progress int }{100}); err != nil {
			log.Printf("Error encoding progress JSON: %v", err)
			http.Error(w, "Error encoding response", http.StatusInternalServerError)
		}
		return
	}

	// Get the current progress percentage from the thread-safe uploadState
	progressPercentage := int(uploadState.GetPercentage())

	// Return the progress percentage as a JSON object
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(struct{ Progress int }{progressPercentage}); err != nil {
		log.Printf("Error encoding progress JSON: %v", err)
		http.Error(w, "Error encoding response", http.StatusInternalServerError)
		return
	}
}
