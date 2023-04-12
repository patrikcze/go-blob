# Go Blob File Uploader
This is a web page that allows users to upload a file to a server using AJAX and displays the upload progress with a progress bar.

## Features
- Allows user to select a file from their local computer to upload
- Displays the upload progress with a progress bar
- When upload is complete, a download link is displayed and copied to the user's clipboard automatically
- If no file is selected when the user tries to upload, an alert message will appear to prompt the user to select a file
- If there is an error during the upload process, an alert message will appear to inform the user
## Technologies Used
- HTML
- CSS
- JavaScript
## Dependencies
- None
## How to Use
1. Clone the repository or download the files to your computer.
2. Open the index.html file in your web browser.
3. Click the "Browse" button to select a file to upload.
4. Click the "Upload" button to upload the selected file.
5. Once the upload is complete, a download link will appear and will be copied to your clipboard automatically.

## Known Issues
- 12.4.2023 - there are known issues with progress bar, it does not actually show UPLOAD progres, rather copy progress to `server-side` which is written in `Go`. 
## License
This project is licensed under the MIT License.