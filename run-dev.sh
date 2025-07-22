#!/bin/bash

# Script to run the Go Blob uploader in development mode
echo "Starting Go Blob File Uploader in Development Mode..."

# Set development mode environment variable
export DEV_MODE=true

# Build and run the application
go build -o go-blob-dev main.go
./go-blob-dev

# Clean up binary after stopping
rm -f go-blob-dev
