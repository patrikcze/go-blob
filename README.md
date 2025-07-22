# Go Blob File Uploader - Modernized Version

## What's New

This modernized version of the Go Blob File Uploader includes significant improvements to both the web interface and Go backend:

### 🎨 Web Interface Improvements
- **Modern Design**: Complete UI overhaul with gradient backgrounds and smooth animations
- **Enhanced UX**: Better file selection feedback with file size display
- **Real-time Progress**: Improved progress bars with smooth transitions
- **Responsive Design**: Mobile-friendly layout that works on all devices
- **Better Error Handling**: Clear error messages and validation feedback

### 🚀 Go Backend Enhancements
- **Large File Support**: Now handles files up to **4GB** (increased from 512MB)
- **Memory Efficiency**: Improved streaming upload with optimized buffer management
- **Better Concurrency**: Enhanced thread-safety and resource management
- **Extended Timeouts**: Appropriate timeouts for large file uploads (30 minutes)
- **Chunked Processing**: Files are processed in 256KB chunks to minimize memory usage

## Features

- ✅ Upload files up to 4GB to Azure Blob Storage
- ✅ Generate time-limited SAS URLs (24 hours)
- ✅ Real-time upload progress tracking
- ✅ Memory-efficient streaming uploads
- ✅ Development mode for local testing
- ✅ Rate limiting and concurrent upload protection
- ✅ Modern, responsive web interface

## Quick Start

### Development Mode (No Azure Required)
```bash
# Run in development mode - files saved locally
./run-dev.sh
```

### Production Mode (With Azure)
```bash
# Set your Azure credentials
export AZURE_STORAGE_ACCOUNT_NAME="yourstorageaccount"
export AZURE_STORAGE_ACCOUNT_KEY="your_access_key"
export AZURE_STORAGE_ACCOUNT_CONTAINER="upload"

# Build and run
go build -o go-blob main.go
./go-blob
```

## Supported File Types
- Images: `.jpg`, `.jpeg`, `.png`, `.gif`
- Documents: `.pdf`, `.doc`, `.docx`, `.xls`, `.xlsx`, `.txt`, `.csv`
- Archives: `.zip`

## Configuration

### Environment Variables
- `AZURE_STORAGE_ACCOUNT_NAME`: Your Azure storage account name
- `AZURE_STORAGE_ACCOUNT_KEY`: Your Azure storage account access key
- `AZURE_STORAGE_ACCOUNT_CONTAINER`: Container name (defaults to "upload")
- `DEV_MODE`: Set to "true" for local development without Azure

### Server Configuration
- **Port**: 9000
- **Max File Size**: 4GB
- **Upload Timeout**: 30 minutes
- **Rate Limit**: 5 requests per second

## Technical Improvements

### Memory Optimization
- Streaming file processing with 256KB buffers
- Reduced memory footprint for large files
- Proper resource cleanup and garbage collection

### Error Handling
- Comprehensive error logging
- Graceful failure recovery
- User-friendly error messages

### Security
- Request size limiting
- File type validation
- Rate limiting protection
- Proper resource cleanup

## Usage

1. Open your browser to `http://localhost:9000`
2. Click "Choose File" to select a file
3. Click "Upload File" to start the upload
4. Monitor progress in real-time
5. Copy the generated download link when complete

## Docker Support

Build container image:
```bash
make docker-build
```

Run in container:
```bash
docker run --rm -p 9000:9000 \
  -e AZURE_STORAGE_ACCOUNT_NAME="youraccount" \
  -e AZURE_STORAGE_ACCOUNT_KEY="yourkey" \
  -e AZURE_STORAGE_ACCOUNT_CONTAINER="upload" \
  go-blob
```

## Development

The application now supports both development and production modes:

- **Development Mode**: Files are stored locally in the `temp/` directory
- **Production Mode**: Files are uploaded to Azure Blob Storage with SAS URL generation

## Performance Notes

- Large files (>1GB) are processed efficiently using streaming uploads
- Memory usage remains constant regardless of file size
- Upload progress is tracked in real-time
- Concurrent uploads are limited to prevent resource exhaustion

## Browser Compatibility

- Chrome 60+
- Firefox 55+
- Safari 11+
- Edge 79+

## License

MIT License - see original repository for details.
