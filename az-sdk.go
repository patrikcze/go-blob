package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blockblob"
)

func azsdk() {
	file, err := os.Open("BigFile.bin") // Open the file we want to upload
	if err != nil {
		log.Fatal(err)
	}
	defer func(file *os.File) {
		err := file.Close()
		if err != nil {
		}
	}(file)
	fileSize, err := file.Stat() // Get the size of the file (stream)
	if err != nil {
		log.Fatal(err)
	}

	// From the Azure portal, get your Storage account blob service URL endpoint.
	accountName, accountKey := os.Getenv("AZURE_STORAGE_ACCOUNT_NAME"), os.Getenv("AZURE_STORAGE_ACCOUNT_KEY")

	// Create a BlockBlobURL object to a blob in the container (we assume the container already exists).
	u := fmt.Sprintf("https://%s.blob.core.windows.net/mycontainer/BigBlockBlob.bin", accountName)
	credential, err := azblob.NewSharedKeyCredential(accountName, accountKey)
	if err != nil {
		log.Fatal(err)
	}
	blockBlobClient, err := blockblob.NewClientWithSharedKeyCredential(u, credential, nil)
	if err != nil {
		log.Fatal(err)
	}

	// Pass the Context, stream, stream size, block blob URL, and options to StreamToBlockBlob
	response, err := blockBlobClient.UploadFile(context.TODO(), file,
		&blockblob.UploadFileOptions{
			// If Progress is non-nil, this function is called periodically as bytes are uploaded.
			Progress: func(bytesTransferred int64) {
				fmt.Printf("Uploaded %d of %d bytes.\n", bytesTransferred, fileSize.Size())
			},
		})
	if err != nil {
		log.Fatal(err)
	}

	_ = response // Avoid compiler's "declared and not used" error
	/*
		// Set up file to download the blob to
		destFileName := "BigFile-downloaded.bin"
		destFile, err := os.Create(destFileName)
		if err != nil {
			log.Fatal(err)
		}
		defer func(destFile *os.File) {
			_ = destFile.Close()

		}(destFile)

		// Perform download
		_, err = blockBlobClient.DownloadFile(context.TODO(), destFile,
			&blob.DownloadFileOptions{
				// If Progress is non-nil, this function is called periodically as bytes are uploaded.
				Progress: func(bytesTransferred int64) {
					fmt.Printf("Downloaded %d of %d bytes.\n", bytesTransferred, fileSize.Size())
				}})

		if err != nil {
			log.Fatal(err)
		}
	*/
}
