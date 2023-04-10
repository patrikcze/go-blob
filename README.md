# Go Blob File Uploader

This is just small project to write simple API to upload files to specific Azure Storage account and Container.  For this purpose simple Web Form will be used. This WebForm consist of Browse and Upload botton.
You just need to pick up file from your drive and it will simply upload it to predefined Storage in Azure. 

At the end it will provide you new SAS URI which can be shared. URI has limited validity (14 Days).

It is planned to run this GO in container or somewhere.

`upload_page.html` is a template page which is used in Go code to render HTML with CSS styles. 

`main.go` is main function of whole project. 


## Requirements 

### Environmnet variables

You need to define following Env vars:

```bash
export AZURE_STORAGE_ACCOUNT_NAME=<TargetStorageAccountName>
export AZURE_STORAGE_ACCOUNT_KEY=<XXXXXXXXXXXXXXXXXXXXXXXXXXX/XXXXXXXXXXXXXXXXXXXXXXX==>
export AZURE_STORAGE_ACCOUNT_CONTAINER=<TargetContainerName>
```

## Current issues

- 10.4.2023 - `Still persist` / Progress and Counter CSS via Javascript does not work.
- 10.4.2023 - `Fixed` / SAS URI links are fully functional and properly formatted. 

## Usage


Compile and execute or simply run :

```bash
go run .
```
Or build within container (will be updated later on).