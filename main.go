package main

import (
	"context"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/Azure/azure-pipeline-go/pipeline"
	"github.com/Azure/azure-storage-blob-go/azblob"
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
	http.HandleFunc("/", fileServer)

	// Start the server
	fmt.Println("Starting server on port 8081...")
	if err := http.ListenAndServe(":8081", nil); err != nil {
		log.Fatalf("failed to start the server: %v", err)
	}
}

func fileServer(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.Error(w, "404 not found.", http.StatusNotFound)
		return
	}
	// Parse the HTML template
	tmpl, err := template.ParseFiles("index.html")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Initialize the template data with an empty progress script
	data = TemplateData{
		//ProgressScript: "",
		Progress: 0,
	}
	switch r.Method {
	case "GET":
		// Serve the upload form
		err = tmpl.Execute(w, data)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	case "POST":
		// Handle the file upload
		if err := r.ParseMultipartForm(512 << 20); err != nil {
			fmt.Println(err)
			return
		}
		file, handler, err := r.FormFile("myFile")
		if err != nil {
			fmt.Println(err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer file.Close()
		fmt.Printf("Received file: %+v\n", handler.Filename)
		// reset some values
		var uploadedBytes int64 = 0
		var percentage float64 = 0.0
		// Get Filename and File size from input file
		var fileSize int64 = handler.Size
		var fileName string = handler.Filename

		// Upload to Azure Storage
		credential, err := azblob.NewSharedKeyCredential(storageAccountName, storageAccountKey)
		if err != nil {
			fmt.Println(err)
			return
		}
		p := azblob.NewPipeline(credential, azblob.PipelineOptions{})
		URL, _ := url.Parse(fmt.Sprintf("https://%s.blob.core.windows.net/%s", storageAccountName, storageContainer))
		containerURL := azblob.NewContainerURL(*URL, p)
		blobURL := containerURL.NewBlockBlobURL(handler.Filename)
		blobHTTPHeaders := azblob.BlobHTTPHeaders{
			ContentType: handler.Header.Get("Content-Type"),
		}
		//Never expiring context
		ctx := context.Background()
		// Progress bar for commandline uploading file.
		// bar := progressbar.DefaultBytes(fileSize, "Uploading")
		// Upload with progress meter
		_, err = blobURL.Upload(ctx, pipeline.NewRequestBodyProgress(file, func(bytesTransferred int64) {
			uploadedBytes += bytesTransferred
			//bar.Add(int(bytesTransferred))
			//fmt.Println("Number of bytes transferred:", bytesTransferred)
			//fmt.Println("Total uploaded bytes:", uploadedBytes)
			percentage = (float64(bytesTransferred) / float64(fileSize)) * 100
			// Update progress Bar :(
			// Execute the template with the updated data, which includes the progress script
			data.Progress = int(percentage)

			//tmpl.Execute(w, data)
			//fmt.Fprintf(w, `Progres(%d);`, int(percentage))
			log.Print("Percentage : ", percentage)
			//fmt.Fprint(w, "<script>updateProgressBar(progressBar,%s)</script>", int(percentage))
			// Run function to update progress
			//updateProgress(w, int(percentage))
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
			fmt.Println(err)
			return
		}

		// Execute the template with the updated data, which includes the progress script
		w.Header().Set("Content-Type", "text/html")
		err = tmpl.Execute(w, data)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		// Generate SAS URI with read-only access
		sasURL := blobURL.URL()
		permissions := azblob.BlobSASPermissions{
			Read: true,
		}
		expiryTime := time.Now().UTC().Add(14 * 24 * time.Hour)

		sasQueryParams, err := azblob.BlobSASSignatureValues{
			Protocol:      azblob.SASProtocolHTTPS,
			Permissions:   permissions.String(),
			ExpiryTime:    expiryTime,
			ContainerName: storageContainer,
			BlobName:      fileName,
		}.NewSASQueryParameters(credential)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		sasToken := sasQueryParams.Encode()
		sasURL.RawQuery = sasToken

		urlToSendToSomeone := fmt.Sprintf("https://%s.blob.core.windows.net/%s/%s?%s",
			storageAccountName, storageContainer, fileName, sasToken)
		// At this point, you can send the urlToSendToSomeone to someone via email or any other mechanism you choose.
		// Return SAS URI to HTML page as a link
		fmt.Fprintf(w, "<h3>File uploaded successfully to Azure Blob Storage!</h3><br />")
		fmt.Fprintf(w, "<a href=\"#\" onclick=\"copyToClipboard('%s')\">Copy Download Link to Clipboard</a><br />", urlToSendToSomeone)
		fmt.Fprintf(w, "<a href=\"%s\" target=\"_blank\">Download File (Link will be valid for 14 Days!)</a><br />", urlToSendToSomeone)
	default:
		fmt.Fprintf(w, "Sorry, only GET and POST methods are supported.")
	}
}

/*
func updateProgress(w http.ResponseWriter, percentage int) {
	// Progress format Javascript script update progress and counter
	progress := fmt.Sprintf(`<script language="JavaScript" type="text/javascript">uploadForm.querySelector('.result').textContent = '%d%%'; </script>`, percentage)
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprint(w, progress)
}
*/
