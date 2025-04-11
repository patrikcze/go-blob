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
	"strings"
	"sync"
	"time"

	_ "github.com/Azure/azure-sdk-for-go/sdk/azcore/streaming"
	_ "github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/sas"
	"github.com/schollz/progressbar/v3"
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

// Update updates the state of the upload with thread safety
func (s *UploadState) Update(bytesTransferred int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.UploadedBytes = bytesTransferred
	if s.FileSize > 0 {
		s.Percentage = (float64(bytesTransferred) / float64(s.FileSize)) * 100
	}
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
)
const (
	// Set buffer size to 512 MB
	maxRequestSize = 512 * 1024 * 1024

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
	}

	// Set up application configuration
	appConfig = AppConfig{
		TempDir:          "temp",
		AllowedFileTypes: allowedTypes,
		MinFileSize:      minFileSize,
		MaxFileSize:      maxRequestSize,
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
	
	// Start the server
	fmt.Println("Starting server on port 9000...")
	server := &http.Server{
		Addr:         ":9000",
		ReadTimeout:  60 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
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

// validateFileType checks if the provided file extension is allowed
// Returns a bool indicating if the type is allowed and the extension string
func validateFileType(filename string) (bool, string) {
	ext := strings.ToLower(filepath.Ext(filename))
	allowed := appConfig.AllowedFileTypes[ext]
	log.Printf("File type validation: %s is %v", ext, allowed)
	return allowed, ext
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
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestSize)
	log.Printf("Setting buffer size to : %v (bytes)", maxRequestSize)
	
	// Parse multipart form
	if err := r.ParseMultipartForm(maxRequestSize); err != nil {
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
			// Validate file type
			if valid, ext := validateFileType(fileHeader.Filename); !valid {
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
			
			// Use a defer with a named function for proper resource cleanup
			defer func() {
				if err := safeClose(file, "uploaded file"); err != nil {
					log.Printf("Failed to close uploaded file: %v", err)
				}
			}()
			// Get Filename and File size from input file
			var fileSize int64 = fileHeader.Size
			var fileName string = fileHeader.Filename
			log.Printf("Received file: %s, size: %d\n", fileName, fileSize)

			// Initialize upload state
			uploadState = UploadState{
				UploadedBytes: 0,
				Percentage:    0,
				FileSize:      fileSize,
			}

			// Create temporary file path
			tempFile := filepath.Join(appConfig.TempDir, fileHeader.Filename)
			
			// Convert multipart.File to os.File
			osFile, err := os.Create(tempFile)
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
			// Use a buffer for more efficient I/O
			buf := make([]byte, 32*1024) // 32KB buffer
			_, err = io.CopyBuffer(osFile, file, buf)
			if err != nil {
				handleError(w, "Error caching the file", err, http.StatusInternalServerError)
				
				// Clean up the temporary file on error
				if removeErr := os.Remove(tempFile); removeErr != nil {
					log.Printf("Error removing temporary file %s: %v", tempFile, removeErr)
				}
				return
			}
			
			// Seek back to beginning of file for upload
			if _, err = osFile.Seek(0, io.SeekStart); err != nil {
				handleError(w, "Error preparing file for upload", err, http.StatusInternalServerError)
				return
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
				
				u := fmt.Sprintf("https://%s.blob.core.windows.net/", appConfig.StorageAccountName)
				
				// Create new client for AzBlob with Shared Key Credentials
				client, err := azblob.NewClientWithSharedKeyCredential(u, credential, &azblob.ClientOptions{})
				if err != nil {
					handleError(w, "Error creating Azure Blob Client", err, http.StatusInternalServerError)
					return
				}
				// Create progress bar with better options
				// Create progress bar with better options
				bar := progressbar.NewOptions64(
					fileSize,
					progressbar.OptionSetDescription("Uploading"),
					progressbar.OptionShowBytes(true),
					progressbar.OptionSetWidth(30),
					progressbar.OptionShowCount(),
					progressbar.OptionSpinnerType(14),
					progressbar.OptionSetTheme(progressbar.Theme{
						Saucer:        "=",
						SaucerHead:    ">",
						SaucerPadding: " ",
						BarStart:      "[",
						BarEnd:        "]",
					}),
				)
				// Create context with timeout for the upload operation
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
				defer cancel() // Ensure context resources are released

				// Upload with progress meter using resumable upload
				_, err = client.UploadFile(ctx, appConfig.StorageContainer, fileName, osFile,
					&azblob.UploadFileOptions{
						BlockSize: BlockBlobMaxStageBlockBytes,
						Progress: func(bytesTransferred int64) {
							// Update the upload state with thread safety
							uploadState.Update(bytesTransferred)
							
							// Update the progress bar
							percentage := uploadState.GetPercentage()
							if err := bar.Set(int(percentage)); err != nil {
								log.Printf("Error setting progress bar: %v", err)
								// We can't return from this callback, so just log the error and continue
								// The upload process should still proceed even if the progress bar fails
							}
						}, // Add closing brace and comma for the Progress function
					})
				if err != nil {
					handleError(w, "Error uploading file to Azure Storage", err, http.StatusInternalServerError)
					return
				}
				log.Printf("Uploaded %d bytes of %d (%.2f%%)", 
					uploadState.UploadedBytes, uploadState.FileSize, uploadState.GetPercentage())
				expiryTime := time.Now().UTC().Add(1 * 24 * time.Hour) // Set Expire time 24 hours
				startTime := time.Now().UTC()
				// Setup Client
				// Generate SAS URL for uploaded file
				blobClient := client.ServiceClient().
					NewContainerClient(appConfig.StorageContainer).
					NewBlobClient(fileName)

				// Generate SAS URL for uploaded file
				s, err := blobClient.GetSASURL(sas.BlobPermissions{
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
				fmt.Fprintf(w, "File stored at: %s<br />", tempFile)
				// File won't be cleaned up in dev mode due to the defer condition above
			}
		}
	}
} // End of handlePost function

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
