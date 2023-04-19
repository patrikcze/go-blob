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
	"time"

	//"github.com/Azure/azure-pipeline-go/pipeline"

	_ "github.com/Azure/azure-sdk-for-go/sdk/azcore/streaming"
	_ "github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/sas"
	"github.com/schollz/progressbar/v3"
	//"github.com/Azure/azure-storage-blob-go/azblob"
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

			// Convert multipart.File to os.File
			osFile, err := os.Create("temp/" + fileHeader.Filename)
			if err != nil {
				log.Printf("Error converting multi-part file %s: %v\n", fileHeader.Filename, err)
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			defer osFile.Close()
			_, err = io.Copy(osFile, file)
			if err != nil {
				log.Printf("Error caching the file %s: %v\n", fileHeader.Filename, err)
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}

			//#######################################
			// Azure SDK
			//#######################################
			// create a Shared Key credential for authenticating with Azure Active Directory
			// Upload to Azure Storage
			credential, err := azblob.NewSharedKeyCredential(storageAccountName, storageAccountKey)
			if err != nil {
				log.Printf("Error creating Azure Storage credentials: %v", err)
				http.Error(w, "Internal Server Error", http.StatusInternalServerError)
				return
			}
			u := fmt.Sprintf("https://%s.blob.core.windows.net/", storageAccountName)

			client, err := azblob.NewClientWithSharedKeyCredential(u, credential, &azblob.ClientOptions{})
			if err != nil {
				log.Printf("Error creating Azure Blob Client with Shared Key: %v", err)
				http.Error(w, "Internal Server Error", http.StatusInternalServerError)
				return
			}

			// Set context and progress bar
			ctx := context.Background()
			bar := progressbar.New(100)

			// Reset some values
			//uploadedBytes := int64(0)
			percentage = float64(0.0)

			// Upload with progress meter using resumable upload
			_, err = client.UploadFile(ctx, storageContainer, fileName, osFile,
				&azblob.UploadFileOptions{
					BlockSize: 4 * 1024 * 1024,
					Progress: func(bytesTransferred int64) {
						uploadedBytes = +bytesTransferred
						percentage = (float64(bytesTransferred) / float64(fileSize)) * 100
						bar.Set(int(percentage))
						//log.Printf("Uploaded %d bytes of %d (%.2f%%)", uploadedBytes, fileSize, percentage)
					},
				})
			if err != nil {
				log.Printf("Error uploading file %s to Azure Storage: %v", fileName, err)
				http.Error(w, "Internal Server Error", http.StatusInternalServerError)
				return
			}
			// Report uploaded file
			log.Printf("Uploaded %d bytes of %d (%.2f%%)", uploadedBytes, fileSize, percentage)
			// Get SAS
			expiryTime := time.Now().UTC().Add(1 * 24 * time.Hour) // Set Expire time 24 hours
			startTime := time.Now().UTC()
			// Setup Client
			// Generate SAS URL
			s, err := client.ServiceClient().
				NewContainerClient(storageContainer).
				NewBlobClient(fileName).
				GetSASURL(sas.BlobPermissions{
					Read: true,
				},
					expiryTime,
					&blob.GetSASURLOptions{
						StartTime: &startTime,
					},
				)
			if err != nil {
				log.Printf("Error generating SAS : %v", err)
				http.Error(w, "Internal Server Error", http.StatusInternalServerError)
				return
			}

			// Remove the file after upload is complete
			defer os.Remove(osFile.Name())
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			fmt.Fprintf(w, "<h3>File uploaded %s successfully to Azure Blob Storage!</h3><br />", fileName)
			fmt.Fprintf(w, "<a href=\"#\" onclick=\"copyToClipboard('%s')\">Copy Download Link to Clipboard</a><br />", s)
			fmt.Fprintf(w, "<a href=\"%s\" target=\"_blank\">Download File (Link will be valid for 1 Day!)</a><br />", s)

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
