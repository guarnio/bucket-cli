package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	s3 "github.com/aws/aws-sdk-go-v2/service/s3"
)

// version is the build version. It can be overridden at build time with
// -ldflags "-X main.version=...".
var version = "0.1.0"

const s3Scheme = "s3://"

func main() {
	log.SetFlags(0)
	log.SetPrefix("bucket-cli: ")

	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "cp":
		cmdCp(os.Args[2:])
	case "version", "-version", "--version", "-v":
		fmt.Printf("bucket-cli %s\n", version)
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "bucket-cli: unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `bucket-cli %s — copy files to/from an S3-compatible store (MinIO, AWS S3, ...)

Usage:
  bucket-cli cp [options] <local-file> s3://<bucket>/[<key>]    upload
  bucket-cli cp [options] s3://<bucket>/<key> [<local-path>]    download
  bucket-cli version

Run "bucket-cli cp -h" for the full list of options.
`, version)
}

// options holds every flag accepted by cp.
type options struct {
	endpoint    string
	region      string
	profile     string
	accessKey   string
	secretKey   string
	useSSL      bool
	insecure    bool
	quiet       bool
	partSize    int64
	concurrency int
}

func (o *options) register(fs *flag.FlagSet) {
	fs.StringVar(&o.endpoint, "endpoint", os.Getenv("AWS_ENDPOINT_URL"), "S3 endpoint, e.g. minio:9000 (or AWS_ENDPOINT_URL; empty = AWS S3)")
	fs.StringVar(&o.region, "region", "", "Region (or AWS_REGION / profile; default us-east-1)")
	fs.StringVar(&o.profile, "profile", "", "Shared config profile from ~/.aws (or AWS_PROFILE)")
	fs.StringVar(&o.accessKey, "access-key", "", "Access key (or AWS_ACCESS_KEY_ID / profile)")
	fs.StringVar(&o.secretKey, "secret-key", "", "Secret key (or AWS_SECRET_ACCESS_KEY / profile)")
	fs.BoolVar(&o.useSSL, "ssl", false, "Use https when -endpoint has no scheme")
	fs.BoolVar(&o.insecure, "insecure", false, "Skip TLS certificate verification (INSECURE)")
	fs.BoolVar(&o.quiet, "quiet", false, "Suppress progress and success output (useful for cron)")
	fs.Int64Var(&o.partSize, "part-size", 0, "Multipart part size in bytes (0 = auto, minimum 5MiB)")
	fs.IntVar(&o.concurrency, "concurrency", manager.DefaultUploadConcurrency, "Number of parts to transfer concurrently")
}

// newClient builds an S3 client. With a custom endpoint (MinIO & co.) it uses
// path-style addressing; without one it talks to AWS S3 directly. Credentials
// and region fall back to the standard AWS chain (env vars, ~/.aws, ...).
func (o *options) newClient() *s3.Client {
	var loadOpts []func(*config.LoadOptions) error
	if o.region != "" {
		loadOpts = append(loadOpts, config.WithRegion(o.region))
	}
	if o.profile != "" {
		loadOpts = append(loadOpts, config.WithSharedConfigProfile(o.profile))
	}
	if o.accessKey != "" || o.secretKey != "" {
		if o.accessKey == "" || o.secretKey == "" {
			log.Fatal("-access-key and -secret-key must be given together")
		}
		loadOpts = append(loadOpts, config.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(o.accessKey, o.secretKey, "")))
	}
	if o.insecure {
		// #nosec G402 -- explicitly requested with -insecure.
		loadOpts = append(loadOpts, config.WithHTTPClient(&http.Client{
			Transport: &http.Transport{
				Proxy:           http.ProxyFromEnvironment,
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
		}))
	}

	cfg, err := config.LoadDefaultConfig(context.TODO(), loadOpts...)
	if err != nil {
		log.Fatalf("unable to load SDK config: %v", err)
	}
	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}

	return s3.NewFromConfig(cfg, func(so *s3.Options) {
		if o.endpoint == "" {
			return
		}
		endpointURL := o.endpoint
		if !strings.HasPrefix(endpointURL, "http://") && !strings.HasPrefix(endpointURL, "https://") {
			scheme := "http"
			if o.useSSL {
				scheme = "https"
			}
			endpointURL = scheme + "://" + endpointURL
		}
		so.BaseEndpoint = aws.String(endpointURL)
		so.UsePathStyle = true // Required for MinIO
	})
}

// parseArgs parses flags and positional arguments in any order, so both
// "cp -quiet a b" and "cp a b -quiet" work (like awscli).
func parseArgs(fs *flag.FlagSet, args []string) []string {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			os.Exit(2)
		}
		args = fs.Args()
		if len(args) == 0 {
			return positional
		}
		positional = append(positional, args[0])
		args = args[1:]
	}
}

// parseS3URI splits "s3://bucket/some/key" into bucket and key. ok is false if
// s does not use the s3:// scheme.
func parseS3URI(s string) (bucket, key string, ok bool) {
	if !strings.HasPrefix(s, s3Scheme) {
		return "", "", false
	}
	bucket, key, _ = strings.Cut(strings.TrimPrefix(s, s3Scheme), "/")
	if bucket == "" {
		log.Fatalf("invalid S3 URI %q: missing bucket", s)
	}
	return bucket, key, true
}

func cmdCp(args []string) {
	fs := flag.NewFlagSet("cp", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `Usage:
  bucket-cli cp [options] <local-file> s3://<bucket>/[<key>]
  bucket-cli cp [options] s3://<bucket>/<key> [<local-path>]

If the destination key is empty or ends with "/", the local file name is
appended. If the local destination is omitted, is a directory or ends with a
path separator, the object's base name is used.

Options:
`)
		fs.PrintDefaults()
	}
	var o options
	o.register(fs)
	pos := parseArgs(fs, args)

	if len(pos) < 1 || len(pos) > 2 {
		fs.Usage()
		os.Exit(2)
	}
	src, dst := pos[0], ""
	if len(pos) == 2 {
		dst = pos[1]
	}

	srcBucket, srcKey, srcIsS3 := parseS3URI(src)
	dstBucket, dstKey, dstIsS3 := parseS3URI(dst)

	switch {
	case srcIsS3 && dstIsS3:
		log.Fatal("copying between two S3 locations is not supported")
	case srcIsS3:
		download(&o, srcBucket, srcKey, dst)
	case dstIsS3:
		upload(&o, src, dstBucket, dstKey)
	default:
		log.Fatal("one of source or destination must be an s3://<bucket>/<key> URI")
	}
}

// upload sends a local file to the bucket using a multipart upload.
func upload(o *options, filePath, bucket, key string) {
	if key == "" || strings.HasSuffix(key, "/") {
		key += filepath.Base(filePath)
	}

	file, err := os.Open(filePath)
	if err != nil {
		log.Fatalf("unable to open file: %v", err)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		log.Fatalf("unable to stat file: %v", err)
	}
	if info.IsDir() {
		log.Fatalf("%s is a directory; only single files are supported", filePath)
	}
	fileSize := info.Size()

	client := o.newClient()

	// Derive a part size that keeps the part count at or below S3's hard limit
	// of manager.MaxUploadParts (10,000). This removes the ~48.8GiB ceiling of a
	// fixed 5MiB part size and lets us upload up to S3's 5TiB per-object maximum.
	uploader := manager.NewUploader(client, func(u *manager.Uploader) {
		u.PartSize = computePartSize(fileSize, o.partSize)
		u.Concurrency = o.concurrency
	})

	cnt := &counter{}
	var body io.Reader = file
	if !o.quiet {
		body = &progressReader{file: file, cnt: cnt}
	}

	err = withProgress(o.quiet, "uploaded", cnt, fileSize, func() error {
		_, e := uploader.Upload(context.TODO(), &s3.PutObjectInput{
			Bucket: aws.String(bucket),
			Key:    aws.String(key),
			Body:   body,
		})
		return e
	})
	if err != nil {
		log.Fatalf("failed to upload: %v", err)
	}

	if !o.quiet {
		fmt.Printf("upload: %s to s3://%s/%s (%s)\n", filePath, bucket, key, humanBytes(fileSize))
	}
}

// download fetches an object from the bucket to a local file.
func download(o *options, bucket, key, dest string) {
	if key == "" || strings.HasSuffix(key, "/") {
		log.Fatalf("s3://%s/%s is not an object key; give the full key of the file to download", bucket, key)
	}

	outPath := dest
	base := path.Base(key)
	switch {
	case outPath == "":
		outPath = base
	case strings.HasSuffix(outPath, "/") || strings.HasSuffix(outPath, string(os.PathSeparator)):
		outPath = filepath.Join(outPath, base)
	default:
		if fi, err := os.Stat(outPath); err == nil && fi.IsDir() {
			outPath = filepath.Join(outPath, base)
		}
	}

	client := o.newClient()

	// Best-effort lookup of the object size so progress can show a percentage.
	total := int64(-1)
	if head, err := client.HeadObject(context.TODO(), &s3.HeadObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	}); err == nil {
		total = aws.ToInt64(head.ContentLength)
	}

	out, err := os.Create(outPath)
	if err != nil {
		log.Fatalf("unable to create file: %v", err)
	}

	downloader := manager.NewDownloader(client, func(d *manager.Downloader) {
		if o.partSize >= manager.DefaultDownloadPartSize {
			d.PartSize = o.partSize
		}
		d.Concurrency = o.concurrency
	})

	cnt := &counter{}
	var w io.WriterAt = out
	if !o.quiet {
		w = &progressWriterAt{w: out, cnt: cnt}
	}

	err = withProgress(o.quiet, "downloaded", cnt, total, func() error {
		_, e := downloader.Download(context.TODO(), w, &s3.GetObjectInput{
			Bucket: aws.String(bucket),
			Key:    aws.String(key),
		})
		return e
	})
	out.Close()
	if err != nil {
		// log.Fatalf calls os.Exit, which skips deferred cleanup, so remove the
		// partial file here before exiting.
		os.Remove(outPath)
		log.Fatalf("failed to download: %v", err)
	}

	if !o.quiet {
		fmt.Printf("download: s3://%s/%s to %s\n", bucket, key, outPath)
	}
}

// computePartSize returns a part size (in bytes) that respects S3's minimum
// part size and guarantees the object is split into at most
// manager.MaxUploadParts parts. A requested size of 0 means "auto".
func computePartSize(fileSize, requested int64) int64 {
	size := requested
	if size < manager.MinUploadPartSize {
		size = manager.DefaultUploadPartSize
	}
	// Smallest part size that fits the file within MaxUploadParts (round up).
	maxParts := int64(manager.MaxUploadParts)
	required := (fileSize + maxParts - 1) / maxParts
	if required > size {
		size = required
	}
	if size < manager.MinUploadPartSize {
		size = manager.MinUploadPartSize
	}
	return size
}

// counter is a goroutine-safe byte counter shared between the I/O wrappers and
// the progress reporter.
type counter struct{ n atomic.Int64 }

func (c *counter) add(d int64) { c.n.Add(d) }
func (c *counter) load() int64 { return c.n.Load() }

// progressReader wraps an *os.File for uploads, counting bytes read while
// preserving io.ReaderAt/io.Seeker so the uploader keeps its concurrent path.
type progressReader struct {
	file *os.File
	cnt  *counter
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.file.Read(b)
	p.cnt.add(int64(n))
	return n, err
}

func (p *progressReader) ReadAt(b []byte, off int64) (int, error) {
	n, err := p.file.ReadAt(b, off)
	p.cnt.add(int64(n))
	return n, err
}

func (p *progressReader) Seek(offset int64, whence int) (int64, error) {
	return p.file.Seek(offset, whence)
}

// progressWriterAt wraps an io.WriterAt for downloads, counting bytes written.
type progressWriterAt struct {
	w   io.WriterAt
	cnt *counter
}

func (p *progressWriterAt) WriteAt(b []byte, off int64) (int, error) {
	n, err := p.w.WriteAt(b, off)
	p.cnt.add(int64(n))
	return n, err
}

// withProgress runs fn while periodically printing transfer progress, unless
// quiet is set. label is the verb shown ("uploaded"/"downloaded"); total may be
// negative if unknown (then only the byte count is shown). Byte counts are
// approximate on retries, so the percentage is capped at 100%.
func withProgress(quiet bool, label string, c *counter, total int64, fn func() error) error {
	if quiet {
		return fn()
	}

	stop := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				printProgress(label, c.load(), total)
			}
		}
	}()

	err := fn()
	close(stop)
	<-finished // make sure the reporter goroutine has stopped printing
	printProgress(label, c.load(), total)
	fmt.Println()
	return err
}

func printProgress(label string, done, total int64) {
	if total > 0 {
		if done > total {
			done = total
		}
		fmt.Printf("\r%s %s / %s (%.1f%%)   ",
			label, humanBytes(done), humanBytes(total), float64(done)/float64(total)*100)
		return
	}
	fmt.Printf("\r%s %s   ", label, humanBytes(done))
}

// humanBytes formats a byte count using binary (IEC) units.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
