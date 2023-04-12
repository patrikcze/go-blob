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
	percentage         float64
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

	// Start the server
	fmt.Println("Starting server on port 9000...")
	if err := http.ListenAndServe(":9000", nil); err != nil {
		log.Fatalf("failed to start the server: %v", err)
	}
}

func handleGet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
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
	err = tmpl.Execute(w, data)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}
func handlePost(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		log.Printf("Method not allowed!")
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// Set buffer size to 512 MB
	const maxRequestSize = 512 * 1024 * 1024
	// Limit the size of the request body to prevent denial of service attacks
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestSize)

	// Handle the file upload
	if err := r.ParseMultipartForm(maxRequestSize); err != nil {
		log.Printf(err.Error())
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	/*
		file, handler, err := r.FormFile("myFile")
		if err != nil {
			log.Printf(err.Error())
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer file.Close()
		fmt.Printf("Received file: %+v\n", handler.Filename)
	*/
	// Process the uploaded files
	for _, fileHeaders := range r.MultipartForm.File {
		for _, fileHeader := range fileHeaders {
			file, err := fileHeader.Open()
			if err != nil {
				log.Printf(err.Error())
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			defer file.Close()
			fmt.Printf("\nReceived file: %+v\n", fileHeader.Filename)
			// Get Filename and File size from input file
			var fileSize int64 = fileHeader.Size
			var fileName string = fileHeader.Filename

			// Upload to Azure Storage
			credential, err := azblob.NewSharedKeyCredential(storageAccountName, storageAccountKey)
			if err != nil {
				fmt.Println(err)
				return
			}
			p := azblob.NewPipeline(credential, azblob.PipelineOptions{})
			URL, _ := url.Parse(fmt.Sprintf("https://%s.blob.core.windows.net/%s", storageAccountName, storageContainer))
			containerURL := azblob.NewContainerURL(*URL, p)
			blobURL := containerURL.NewBlockBlobURL(fileHeader.Filename)
			blobHTTPHeaders := azblob.BlobHTTPHeaders{
				ContentType: fileHeader.Header.Get("Content-Type"),
			}
			//Never expiring context
			ctx := context.Background()
			// Progress bar for commandline uploading file.
			bar := progressbar.New(100)

			// reset some values
			uploadedBytes = 0
			percentage = 0.0

			// Upload with progress meter
			_, err = blobURL.Upload(ctx, pipeline.NewRequestBodyProgress(file, func(bytesTransferred int64) {
				uploadedBytes += bytesTransferred
				//fmt.Println("Number of bytes transferred:", bytesTransferred)
				//fmt.Println("Total uploaded bytes:", uploadedBytes)
				percentage = (float64(bytesTransferred) / float64(fileSize)) * 100
				//Set The Progress
				//data.Progress = int(percentage)
				// Update progress Console Progress Bar
				bar.Set(int(percentage))
				//tmpl.Execute(w, data)
				//fmt.Fprintf(w, `Progres(%d);`, int(percentage))
				//log.Print("Percentage : ", percentage)
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
			//fmt.Println("Uploaded")
			bar.State()
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
			// Encoded query values withou ?
			sasURL.RawQuery = sasToken
			// Add SAS query values to the URL
			urlToSendToSomeone := fmt.Sprintf("https://%s.blob.core.windows.net/%s/%s?%s",
				storageAccountName, storageContainer, fileName, sasToken)
			// At this point, you can send the urlToSendToSomeone to someone via email or any other mechanism you choose.
			// Return SAS URI to HTML page as a link
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprintf(w, "<h3>File uploaded successfully to Azure Blob Storage!</h3><br />")
			fmt.Fprintf(w, "<a href=\"#\" onclick=\"copyToClipboard('%s')\">Copy Download Link to Clipboard</a><br />", urlToSendToSomeone)
			fmt.Fprintf(w, "<a href=\"%s\" target=\"_blank\">Download File (Link will be valid for 14 Days!)</a><br />", urlToSendToSomeone)
		}
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
/*
#################################################
 WEB SOCKET FUNCTION UPDATE IF NEEDED
#################################################
 JAVASCRIPT IN HTML TO LISTEN:

 <script>
    $(document).ready(function() {
        // Open WebSocket connection
        const ws = new WebSocket("ws://" + window.location.host + "/upload");

        // Listen for progress updates
        ws.onmessage = function(event) {
            const data = JSON.parse(event.data);
            const progress = data.progress;
            $('.progress-bar').css('--width', progress);
            $('.progress-bar').attr('data-label', `Upload... ${progress}%`);
        };
    });
</script>
#################################################
*/
/*
func uploadHandler(w http.ResponseWriter, r *http.Request) {
	// Upgrade connection to WebSocket
	upgrader := websocket.Upgrader{}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		// Handle error
	}

	// Periodically send progress updates
	for i := 0; i <= 100; i += 10 {
		progress := i
		message := fmt.Sprintf(`{"progress": %d}`, progress)
		err := conn.WriteMessage(websocket.TextMessage, []byte(message))
		if err != nil {
			// Handle error
		}
		time.Sleep(time.Second)
	}

	// Close WebSocket connection
	conn.Close()
}
*/
