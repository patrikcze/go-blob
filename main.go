package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/Azure/azure-storage-blob-go/azblob"
)

var (
	storageAccountName string
	storageAccountKey  string
	storageContainer   string
)

const (
	maxRequestSize = 512 * 1024 * 1024 // 512 MB
	blockSize      = 4 * 1024 * 1024   // 4 MB
)

// Initialize the Azure Blob Storage credentials
func init() {
	storageAccountName = os.Getenv("AZURE_STORAGE_ACCOUNT_NAME")
	storageAccountKey = os.Getenv("AZURE_STORAGE_ACCOUNT_KEY")
	storageContainer = os.Getenv("AZURE_STORAGE_ACCOUNT_CONTAINER")

	if storageAccountName == "" || storageAccountKey == "" || storageContainer == "" {
		log.Fatal("Azure storage credentials are not set.")
	}
}

func main() {
	http.HandleFunc("/", handleGet)
	http.HandleFunc("/upload", handlePost)

	fmt.Println("Starting server on port 9000...")
	if err := http.ListenAndServe(":9000", nil); err != nil {
		log.Fatalf("failed to start the server: %v", err)
	}
}

func handleGet(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, "index.html")
}

func handlePost(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Limit the size of the request body
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestSize)

	if err := r.ParseMultipartForm(maxRequestSize); err != nil {
		http.Error(w, "File size limit exceeded", http.StatusBadRequest)
		return
	}

	file, fileHeader, err := r.FormFile("myFile")
	if err != nil {
		http.Error(w, "Failed to get uploaded file", http.StatusBadRequest)
		return
	}
	defer file.Close()

	credential, err := azblob.NewSharedKeyCredential(storageAccountName, storageAccountKey)
	if err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	p := azblob.NewPipeline(credential, azblob.PipelineOptions{})
	URL, _ := url.Parse(fmt.Sprintf("https://%s.blob.core.windows.net/%s", storageAccountName, storageContainer))
	containerURL := azblob.NewContainerURL(*URL, p)
	blobURL := containerURL.NewBlockBlobURL(fileHeader.Filename)

	// Upload file in chunks
	ctx := context.Background()

	var blockIDs []string
	var totalBytesRead int64 = 0
	blockIndex := 0

	for {
		block := make([]byte, blockSize)
		n, err := file.Read(block)
		if err != nil && err != io.EOF {
			http.Error(w, fmt.Sprintf("Error reading file: %v", err), http.StatusInternalServerError)
			return
		}
		if n == 0 {
			break
		}
		totalBytesRead += int64(n)

		// Generate a unique block ID based on block index
		blockID := generateBlockID(blockIndex)
		blockIndex++
		blockIDs = append(blockIDs, blockID)

		// Only use the read portion of the block
		reader := bytes.NewReader(block[:n])

		_, err = blobURL.StageBlock(ctx, blockID, reader, azblob.LeaseAccessConditions{}, nil, azblob.ClientProvidedKeyOptions{})
		if err != nil {
			log.Printf("Error uploading block %s: %v", blockID, err)
			http.Error(w, fmt.Sprintf("Error uploading block: %v", err), http.StatusInternalServerError)
			return
		}
		log.Printf("Uploaded block %s with size %d bytes", blockID, n)
	}

	log.Printf("Total bytes read from file: %d", totalBytesRead)

	_, err = blobURL.CommitBlockList(ctx, blockIDs, azblob.BlobHTTPHeaders{ContentType: fileHeader.Header.Get("Content-Type")}, azblob.Metadata{}, azblob.BlobAccessConditions{}, azblob.DefaultAccessTier, azblob.BlobTagsMap{}, azblob.ClientProvidedKeyOptions{}, azblob.ImmutabilityPolicyOptions{})
	if err != nil {
		log.Printf("Error committing block list: %v", err)
		http.Error(w, fmt.Sprintf("Error committing block list: %v", err), http.StatusInternalServerError)
		return
	}

	// Generate the proper SAS URL
	sasURL, err := generateSASURL(blobURL, credential, fileHeader.Filename)
	if err != nil {
		http.Error(w, fmt.Sprintf("Error generating SAS URL: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, "<h3>File uploaded successfully to Azure Blob Storage!</h3><br />")
	fmt.Fprintf(w, "<a href=\"#\" onclick=\"copyToClipboard('%s')\">Copy Download Link to Clipboard</a><br />", sasURL)
	fmt.Fprintf(w, "<a href=\"%s\" target=\"_blank\">Download File (Link will be valid for 1 Day!)</a><br />", sasURL)
}

func generateBlockID(blockIndex int) string {
	// Create a block ID with a fixed-length format and encode it with Base64
	blockID := fmt.Sprintf("%08d", blockIndex)
	return base64.StdEncoding.EncodeToString([]byte(blockID))
}

func generateSASURL(blobURL azblob.BlockBlobURL, credential *azblob.SharedKeyCredential, fileName string) (string, error) {
	permissions := azblob.BlobSASPermissions{Read: true}
	expiryTime := time.Now().UTC().Add(1 * 24 * time.Hour)
	sasQueryParams, err := azblob.BlobSASSignatureValues{
		Protocol:      azblob.SASProtocolHTTPS, // The protocol (https only)
		Permissions:   permissions.String(),    // Permissions string
		ExpiryTime:    expiryTime,              // Expiry time for the SAS token
		ContainerName: storageContainer,        // The container name
		BlobName:      fileName,                // The blob name
	}.NewSASQueryParameters(credential)

	if err != nil {
		return "", err
	}

	// Construct the URL with SAS token
	u := blobURL.URL()
	q := u.Query()
	q.Set("sv", sasQueryParams.Version())
	q.Set("sr", sasQueryParams.Resource())
	q.Set("sig", sasQueryParams.Signature())
	q.Set("se", sasQueryParams.ExpiryTime().Format(time.RFC3339))
	q.Set("sp", sasQueryParams.Permissions())
	q.Set("spr", string(sasQueryParams.Protocol()))

	// Handle the IP range if specified
	ipRange := sasQueryParams.IPRange()
	if ipRange.Start != nil && ipRange.End != nil {
		q.Set("sip", fmt.Sprintf("%s-%s", ipRange.Start.String(), ipRange.End.String()))
	} else if ipRange.Start != nil {
		q.Set("sip", ipRange.Start.String())
	}

	u.RawQuery = q.Encode()

	blobURLWithSAS := u.String()
	log.Printf("Generated SAS URL: %s", blobURLWithSAS)
	return blobURLWithSAS, nil
}
