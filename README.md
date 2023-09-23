<h1 align="center">Teldrive Upload</h1>

**Upload file parts concurrentlly  in multiple threads to get faster uploads.Default is 4.**
### How To Use

**Follow Below Steps**
- Create the `upload.env` file with variables given below

```shell
API_URL="http://localhost:8000" # url of hosted app
SESSION_TOKEN="" #user session token which can be fetched from teldrive app from cookies
PART_SIZE=1GB # Allowed sizes 500MB,1GB,2GB (1GB is default)
WORKERS=4 # Number of current worker to use when uploading multi-part of a big file, increase this to attain higher speeds with large files (4 is default)
```
- Smaller part size will give max upload speed.
- Download release binary of teldrive upload from releases section.

```shell
./uploader -path "" -dest ""
```

- **-path**  here you can pass single file or folder path.
- **-dest** is remote output path where files will  be saved.
