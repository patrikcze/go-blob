package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"time"

	//"github.com/Azure/azure-pipeline-go/pipeline"
	"github.com/Azure/azure-pipeline-go/pipeline"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"

	//"github.com/Azure/azure-storage-blob-go/azblob"

	"github.com/schollz/progressbar/v3"
)

// Define a struct to hold the template data
type TemplateData struct {
	Progress int
}

// Global variables
var (
	storageAccountName string
	storageAccountKey  string
	storageContainer   string
	data               TemplateData
	uploadedBytes      int64
	percentage         = 0.0
)

const (
	// BlockBlobMaxUploadBlobBytes indicates the maximum number of bytes that can be sent in a call to Upload.
	BlockBlobMaxUploadBlobBytes = 256 * 1024 * 1024 // 256MB

	// BlockBlobMaxStageBlockBytes indicates the maximum number of bytes that can be sent in a call to StageBlock.
	BlockBlobMaxStageBlockBytes = 4000 * 1024 * 1024 // 4000MiB

	// BlockBlobMaxBlocks indicates the maximum number of blocks allowed in a block blob.
	BlockBlobMaxBlocks = 50000

	//"2017-07-27T00:00:00Z" // ISO 8601
	SASTimeFormat = "2006-01-02T15:04:05Z"
)

// Initialize the Azure Blob Storage credentials
func init() {
	// Ensure that storageAccountName, storageAccountKey and storageContainer are not empty
	if os.Getenv("AZURE_STORAGE_ACCOUNT_NAME") == "" {
		log.Fatal("AZURE_STORAGE_ACCOUNT_NAME is not set.")
	}
	if os.Getenv("AZURE_STORAGE_ACCOUNT_KEY") == "" {
		log.Fatal("AZURE_STORAGE_ACCOUNT_KEY is not set.")
	}
	if os.Getenv("AZURE_STORAGE_ACCOUNT_CONTAINER") == "" {
		log.Fatal("AZURE_STORAGE_ACCOUNT_CONTAINER is not set.")
	}

	// Get the storage account credentials from the environment variables
	storageAccountName = os.Getenv("AZURE_STORAGE_ACCOUNT_NAME")
	storageAccountKey = os.Getenv("AZURE_STORAGE_ACCOUNT_KEY")
	storageContainer = os.Getenv("AZURE_STORAGE_ACCOUNT_CONTAINER")
}

func main() {
	// Create a file server
	http.HandleFunc("/", handleGet)
	http.HandleFunc("/upload", handlePost)
	http.HandleFunc("/progress", progressHandler)
	// Start the server
	fmt.Println("Starting server on port 9000...")
	if err := http.ListenAndServe(":9000", nil); err != nil {
		log.Fatalf("failed to start the server: %v", err)
	}
}

func handleGet(w http.ResponseWriter, r *http.Request) {
	log.Printf("Handling GET request from %s", r.RemoteAddr)
	if r.Method != http.MethodGet {
		log.Printf("Method not allowed: %s", r.Method)
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse the HTML template
	log.Print("Parsing HTML template...")
	tmpl, err := template.ParseFiles("index.html")
	if err != nil {
		log.Printf("Error parsing HTML template: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Initialize the template data with an empty progress script
	log.Print("Initializing template data...")
	data := TemplateData{
		//ProgressScript: "",
		Progress: 0,
	}

	// Execute the template
	log.Print("Executing template...")
	err = tmpl.Execute(w, data)
	if err != nil {
		log.Printf("Error executing template: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	log.Print("GET request successfully handled")
}

func handlePost(w http.ResponseWriter, r *http.Request) {
	// Check if the method is POST
	if r.Method != http.MethodPost {
		log.Printf("Method not allowed!")
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Set buffer size to 512 MB
	const maxRequestSize = 512 * 1024 * 1024

	// Limit the size of the request body to prevent denial of service attacks
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestSize)

	// Parse multipart form
	if err := r.ParseMultipartForm(maxRequestSize); err != nil {
		if err.Error() == "http: request body too large" {
			log.Printf("File size limit exceeded %v", err)
			http.Error(w, "File size limit exceeded", http.StatusBadRequest)
			return
		}
		log.Printf("Error parsing multipart form: %v\n", err)
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	// Process the uploaded files
	for _, fileHeaders := range r.MultipartForm.File {
		for _, fileHeader := range fileHeaders {
			// Open the file
			file, err := fileHeader.Open()
			if err != nil {
				log.Printf("Error opening file %s: %v\n", fileHeader.Filename, err)
				http.Error(w, "Internal Server Error", http.StatusInternalServerError)
				return
			}
			defer file.Close()

			// Get Filename and File size from input file
			var fileSize int64 = fileHeader.Size
			var fileName string = fileHeader.Filename
			log.Printf("Received file: %s, size: %d\n", fileName, fileSize)
			URL, err := url.Parse(fmt.Sprintf("https://%s.blob.core.windows.net/%s", storageAccountName, storageContainer))
			client, err := azblob.NewClient("https://MYSTORAGEACCOUNT.blob.core.windows.net/", cred, nil)

			// Upload to Azure Storage
			credential, err := azblob.NewSharedKeyCredential(storageAccountName, storageAccountKey)
			if err != nil {
				log.Printf("Error creating Azure Storage credentials: %v\n", err)
				http.Error(w, "Internal Server Error", http.StatusInternalServerError)
				return
			}

			p := azblob.NewClient().NewPipeline(credential, azblob.PipelineOptions{})
			URL, err := url.Parse(fmt.Sprintf("https://%s.blob.core.windows.net/%s", storageAccountName, storageContainer))
			if err != nil {
				log.Printf("Error parsing Azure Storage URL: %v\n", err)
				http.Error(w, "Internal Server Error", http.StatusInternalServerError)
				return
			}

			containerURL := azblob.NewContainerURL(*URL, p)
			blobURL := containerURL.NewBlockBlobURL(fileHeader.Filename)
			blobHTTPHeaders := azblob.BlobHTTPHeaders{
				ContentType: fileHeader.Header.Get("Content-Type"),
			}

			// Set context and progress bar
			ctx := context.Background()
			bar := progressbar.New(100)

			// Reset some values
			uploadedBytes := int64(0)
			percentage = float64(0.0)

			// Upload the file to the block blob
			_, err = azblob.clie.UploadStreamToBlockBlob(ctx, file, blobURL, azblob.UploadToBlockBlobOptions{
				BufferSize: 4 * 1024 * 1024,
				MaxBuffers: 3,
				Metadata:   nil,
				BlobHTTPHeaders: azblob.BlobHTTPHeaders{
					ContentType: blobHTTPHeaders.ContentType,
				},
				Progress: func(bytesTransferred int64) {
					// Update the progress percentage (stored in global variable)
					uploadedBytes += bytesTransferred
					percentage = (float64(uploadedBytes) / float64(fileSize)) * 100
					log.Printf("Uploaded %d bytes of %d (%.2f%%)", uploadedBytes, fileSize, percentage)
				},
			})
			if err != nil {
				log.Printf("Error uploading file %s to Azure Storage: %v", fileHeader.Filename, err)
				http.Error(w, "Internal Server Error", http.StatusInternalServerError)
				return
			}

			// Upload with progress meter
			_, err = blobURL.Upload(ctx, pipeline.NewRequestBodyProgress(file, func(bytesTransferred int64) {
				uploadedBytes += bytesTransferred
				percentage = (float64(bytesTransferred) / float64(fileSize)) * 100
				bar.Set(int(percentage))
				log.Printf("Uploaded %d bytes of %d (%.2f%%)", bytesTransferred, fileSize, percentage)
			}),
				blobHTTPHeaders,
				azblob.Metadata{},
				azblob.BlobAccessConditions{},
				azblob.DefaultAccessTier,
				nil,
				azblob.ClientProvidedKeyOptions{},
				azblob.ImmutabilityPolicyOptions{},
			)
			if err != nil {
				log.Printf("Error uploading file %s to Azure Storage: %v\n", fileHeader.Filename, err)
				http.Error(w, "Internal Server Error", http.StatusInternalServerError)
				return
			}
			/*
				// Execute the template with the updated data, which includes the progress script
				err = tmpl.Execute(w, data)
				if err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
			*/
			// Generate SAS URI with read-only access
			sasURL := blobURL.URL()
			permissions := azblob.BlobSASPermissions{
				Read: true,
			}
			expiryTime := time.Now().UTC().Add(1 * 24 * time.Hour)
			sasQueryParams, err := azblob.BlobSASSignatureValues{
				Protocol:      azblob.SASProtocolHTTPS,
				Permissions:   permissions.String(),
				ExpiryTime:    expiryTime,
				ContainerName: storageContainer,
				BlobName:      fileName,
			}.NewSASQueryParameters(credential)
			if err != nil {
				log.Printf("Error generating SAS query parameters: %v", err)
				http.Error(w, "Internal Server Error", http.StatusInternalServerError)
				return
			}
			sasToken := sasQueryParams.Encode()
			// Encoded query values withou ?
			sasURL.RawQuery = sasToken
			// Add SAS query values to the URL
			urlToSendToSomeone := fmt.Sprintf("https://%s.blob.core.windows.net/%s/%s?%s",
				storageAccountName, storageContainer, fileName, sasToken)
			// At this point, you can send the urlToSendToSomeone to someone via email or any other mechanism you choose.
			// Return SAS URI to HTML page as a link
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			fmt.Fprintf(w, "<h3>File uploaded successfully to Azure Blob Storage!</h3><br />")
			fmt.Fprintf(w, "<a href=\"#\" onclick=\"copyToClipboard('%s')\">Copy Download Link to Clipboard</a><br />", urlToSendToSomeone)
			fmt.Fprintf(w, "<a href=\"%s\" target=\"_blank\">Download File (Link will be valid for 1 Day!)</a><br />", urlToSendToSomeone)
			//reset percentage:
			percentage = float64(0.0)
		}

	}

}

func progressHandler(w http.ResponseWriter, r *http.Request) {
	// Calculate the progress percentage (assumes the progress is stored in a global variable)
	progressPercentage := int(percentage)

	// Return the progress percentage as a JSON object
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(struct{ Progress int }{progressPercentage})
}

// Upload to Azure Storage using io.Copy and azblob.UploadStreamToBlockBlob
func uploadToBlobUsingStream(ctx context.Context, fileName string, fileSize int64, containerURL azblob.ContainerURL, file io.Reader) error {
	// Create a new block blob
	blobURL := containerURL.NewBlockBlobURL(fileName)

	// Set blob headers
	blobHTTPHeaders := azblob.BlobHTTPHeaders{
		ContentType: "application/octet-stream",
	}

	// Set block size to 4MB
	blockSize := BlockBlobMaxStageBlockBytes / 1000

	// Create a transfer manager
	transferManager := azblob.NewBlobTransferManager(blobURL, azblob.NewPipeline(azblob.NewAnonymousCredential(), azblob.PipelineOptions{}))

	// Upload the file using io.Copy function
	// Create a block blob
	blockIDs := make([]string, 0, 0)
	offset := int64(0)
	buffer := make([]byte, blockSize)
	for {
		bytesRead, err := file.Read(buffer)
		if err != nil {
			if err != io.EOF {
				return fmt.Errorf("failed to read %s: %v", fileName, err)
			}
			break
		}
		reader := bytes.NewReader(buffer[:bytesRead])
		blockID := base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%10d", offset/blockSize)))
		log.Printf("Uploading block %s, size: %d bytes", blockID, bytesRead)
		err = transferManager.UploadStreamToBlockBlob(ctx, reader, blockID, blobHTTPHeaders, azblob.Metadata{}, azblob.BlobAccessConditions{}, nil)
		if err != nil {
			return fmt.Errorf("failed to upload block %s: %v", blockID, err)
		}
		blockIDs = append(blockIDs, blockID)
		offset += int64(bytesRead)
	}
	log.Printf("All blocks uploaded. Finalizing block list.\n")

	// Commit the blocks
	_, err := blobURL.CommitBlockList(ctx, blockIDs, blobHTTPHeaders, azblob.Metadata{}, azblob.BlobAccessConditions{})
	if err != nil {
		return fmt.Errorf("failed to commit block list: %v", err)
	}

	return nil
}

/*
Function returns the percentage of Blob upload progress.
perc (int): The calculated progress as a percentage.
*/
/* func progressUpdate(w http.ResponseWriter, perc int) {
	w.Header().Set("Content-Type", "text/javascript")
	fmt.Fprintf(w, "<script>updateProgressBar</script>")
}
*/
/*
func updateProgress(w http.ResponseWriter, percentage int) {
	// Progress format Javascript script update progress and counter
	progress := fmt.Sprintf(`<script language="JavaScript" type="text/javascript">uploadForm.querySelector('.result').textContent = '%d%%'; </script>`, percentage)
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprint(w, progress)
}
*/
