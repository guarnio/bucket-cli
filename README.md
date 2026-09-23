# bucket-cli

A small Go CLI that copies files **to and from** an S3-compatible object store
(MinIO, AWS S3, ...) with an `aws s3 cp`-like syntax. It replaces the old
`uploader` and `downloader` tools.

```sh
bucket-cli cp ./archive.tar.gz s3://backups/archives/   # upload
bucket-cli cp s3://backups/archives/archive.tar.gz .    # download
```

## Features

- **awscli-style paths**: `s3://<bucket>/<key>` on one side, a local path on
  the other. The direction (upload or download) depends on which side is the S3 URI.
- **No practical size limit on uploads.** The multipart part size is derived
  from the file size, so an upload never goes over S3's limit of 10,000 parts
  (up to the 5 TiB per-object maximum).
- **Multipart, concurrent transfers** via `feature/s3/manager`.
- **MinIO first, AWS too**: with `-endpoint` / `AWS_ENDPOINT_URL` set, it uses
  path-style addressing (what MinIO expects). Without it, it talks to AWS S3
  directly.
- **Standard AWS credential chain**: flags, `AWS_*` env vars, `~/.aws` profiles.
- **Progress reporting**, or fully silent with `-quiet` for cron jobs.

## Build

```sh
make all            # linux/amd64, linux/arm64, darwin/arm64, windows/amd64 into ./bin
make darwin-arm64   # a single target
make clean | tidy | help
go build -o bucket-cli .   # just for this machine
```

Override the stamped version with `make all VERSION=1.2.3`, and check it with
`bucket-cli version`.

## Usage

```
bucket-cli cp [options] <local-file> s3://<bucket>/[<key>]    # upload
bucket-cli cp [options] s3://<bucket>/<key> [<local-path>]    # download
```

Options can go before or after the paths.

**Destination rules** (same as awscli):

| Command                                   | Result                           |
|-------------------------------------------|----------------------------------|
| `cp f.txt s3://b/`                        | uploads to `s3://b/f.txt`        |
| `cp f.txt s3://b/dir/`                    | uploads to `s3://b/dir/f.txt`    |
| `cp f.txt s3://b/dir/g.txt`               | uploads to `s3://b/dir/g.txt`    |
| `cp s3://b/dir/f.txt`                     | downloads to `./f.txt`           |
| `cp s3://b/dir/f.txt some/dir` (existing) | downloads to `some/dir/f.txt`    |
| `cp s3://b/dir/f.txt out.txt`             | downloads to `out.txt`           |

Only single files are supported. There is no recursive copy, and no copy from
one S3 location to another.

### Options

| Flag           | Env fallback            | Default     | Description                                                   |
|----------------|-------------------------|-------------|---------------------------------------------------------------|
| `-endpoint`    | `AWS_ENDPOINT_URL`      | — (AWS S3)  | Custom endpoint, e.g. `minio.example.com:9000` or a full URL. |
| `-region`      | `AWS_REGION` / profile  | `us-east-1` | Region.                                                       |
| `-profile`     | `AWS_PROFILE`           | —           | Profile from `~/.aws/config` / `~/.aws/credentials`.          |
| `-access-key`  | `AWS_ACCESS_KEY_ID`     | —           | Access key (set together with `-secret-key`).                 |
| `-secret-key`  | `AWS_SECRET_ACCESS_KEY` | —           | Secret key.                                                   |
| `-ssl`         | —                       | `false`     | Use `https` when the endpoint has no scheme (default `http`). |
| `-insecure`    | —                       | `false`     | Skip TLS certificate verification (self-signed certs).        |
| `-concurrency` | —                       | `5`         | Number of parts transferred at the same time.                 |
| `-part-size`   | —                       | `0` (auto)  | Part size in bytes (minimum 5 MiB).                           |
| `-quiet`       | —                       | `false`     | No progress/success output (errors are still printed).        |

### Examples

**MinIO, everything on the command line:**

```sh
bucket-cli cp -endpoint minio.bcgaudio.com:9000 -ssl -insecure \
  -access-key admin -secret-key password \
  ./terraform.tfstate s3://terraform-states/prod/
```

**MinIO with env variables:**

```sh
export AWS_ACCESS_KEY_ID=admin
export AWS_SECRET_ACCESS_KEY=password
export AWS_ENDPOINT_URL=https://minio.bcgaudio.com:9000

bucket-cli cp ./backup.tar.gz s3://backups/2026/ -insecure
bucket-cli cp s3://backups/2026/backup.tar.gz /tmp/ -insecure
```

**AWS S3** (leave `AWS_ENDPOINT_URL` unset):

```sh
bucket-cli cp -profile work -region eu-west-1 ./report.pdf s3://my-bucket/reports/
```

**Cron job** (silent unless something fails):

```sh
30 2 * * *  AWS_ACCESS_KEY_ID=... AWS_SECRET_ACCESS_KEY=... AWS_ENDPOINT_URL=https://s3.example.com \
  /usr/local/bin/bucket-cli cp -quiet /var/backups/db.dump s3://backups/db/
```

## Notes

- If a download fails, the partly written local file is deleted.
- A file that already exists at the local destination is overwritten.
