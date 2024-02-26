/*
Copyright 2017 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package gcs

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"k8s.io/apimachinery/pkg/util/sets"
	"mime"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
	"golang.org/x/sync/semaphore"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	utilpointer "k8s.io/utils/pointer"

	pkgio "k8s.io/test-infra/prow/io"
	"k8s.io/test-infra/prow/io/providers"
)

// UploadFunc knows how to upload into an object
type UploadFunc func(writer dataWriter) error

type ReaderFunc func() (io.ReadCloser, error)

type destToWriter func(dest string) dataWriter

const retryCount = 4

// Upload uploads all the data in the uploadTargets map to blob storage in parallel.
// The map is keyed on blob storage path under the bucket.
// Files with an extension in the compressFileTypes list will be compressed prior to uploading
func Upload(ctx context.Context, bucket, gcsCredentialsFile, s3CredentialsFile string, compressFileTypes []string, uploadTargets map[string]UploadFunc) error {
	parsedBucket, err := url.Parse(bucket)
	if err != nil {
		return fmt.Errorf("cannot parse bucket name %s: %w", bucket, err)
	}
	if parsedBucket.Scheme == "" {
		parsedBucket.Scheme = providers.GS
	}

	opener, err := pkgio.NewOpener(ctx, gcsCredentialsFile, s3CredentialsFile)
	if err != nil {
		return fmt.Errorf("new opener: %w", err)
	}
	dtw := func(dest string) dataWriter {
		compressFileTypeSet := sets.NewString(compressFileTypes...)
		extIndex := strings.LastIndex(dest, ".")
		var ext string
		if extIndex >= 0 {
			ext = dest[extIndex+1:]
		}
		compress := compressFileTypeSet.Has("*") || compressFileTypeSet.Has(ext)
		return &openerObjectWriter{Opener: opener, Context: ctx, Bucket: parsedBucket.String(), Dest: dest, compress: compress}
	}
	return upload(dtw, uploadTargets)
}

// LocalExport copies all of the data in the uploadTargets map to local files in parallel. The map
// is keyed on file path under the exportDir.
func LocalExport(ctx context.Context, exportDir string, uploadTargets map[string]UploadFunc) error {
	opener, err := pkgio.NewOpener(ctx, "", "")
	if err != nil {
		return fmt.Errorf("new opener: %w", err)
	}
	dtw := func(dest string) dataWriter {
		return &openerObjectWriter{Opener: opener, Context: ctx, Bucket: exportDir, Dest: dest}
	}
	return upload(dtw, uploadTargets)
}

func upload(dtw destToWriter, uploadTargets map[string]UploadFunc) error {
	errCh := make(chan error, len(uploadTargets))
	group := &sync.WaitGroup{}
	sem := semaphore.NewWeighted(4)
	group.Add(len(uploadTargets))
	for dest, upload := range uploadTargets {
		writer := dtw(dest)
		log := logrus.WithField("dest", writer.fullUploadPath())
		log.Info("Queued for upload")
		go func(f UploadFunc, writer dataWriter, log *logrus.Entry) {
			defer group.Done()

			var err error

			for retryIndex := 1; retryIndex <= retryCount; retryIndex++ {
				err = func() error {
					sem.Acquire(context.Background(), 1)
					defer sem.Release(1)
					if retryIndex > 1 {
						log.WithField("retry_attempt", retryIndex).Debugf("Retrying upload")
					}
					return f(writer)
				}()

				if err == nil {
					break
				}
				if retryIndex < retryCount {
					time.Sleep(time.Duration(retryIndex*retryIndex) * time.Second)
				}
			}

			if err != nil {
				errCh <- err
				log.Info("Failed upload")
			} else {
				log.Info("Finished upload")
			}
		}(upload, writer, log)
	}
	group.Wait()
	close(errCh)
	if len(errCh) != 0 {
		var uploadErrors []error
		for err := range errCh {
			uploadErrors = append(uploadErrors, err)
		}
		return fmt.Errorf("encountered errors during upload: %v", uploadErrors)
	}
	return nil
}

// FileUpload returns an UploadFunc which copies all
// data from the file on disk to the GCS object
func FileUpload(file string) UploadFunc {
	return FileUploadWithOptions(file, pkgio.WriterOptions{})
}

// FileUploadWithOptions returns an UploadFunc which copies all data
// from the file on disk into GCS object and also sets the provided
// attributes on the object.
func FileUploadWithOptions(file string, opts pkgio.WriterOptions) UploadFunc {
	return func(writer dataWriter) error {
		if fi, err := os.Stat(file); err == nil {
			opts.BufferSize = utilpointer.Int64(fi.Size())
			if *opts.BufferSize > 25*1024*1024 {
				*opts.BufferSize = 25 * 1024 * 1024
			}
		}

		newReader := func() (io.ReadCloser, error) {
			reader, err := os.Open(file)
			if err != nil {
				return nil, err
			}
			return reader, nil
		}

		uploadErr := DataUploadWithOptions(newReader, opts)(writer)
		if uploadErr != nil {
			uploadErr = fmt.Errorf("upload error: %w", uploadErr)
		}
		return uploadErr
	}
}

// DataUpload returns an UploadFunc which copies all
// data from src reader into GCS.
func DataUpload(newReader ReaderFunc) UploadFunc {
	return DataUploadWithOptions(newReader, pkgio.WriterOptions{})
}

// DataUploadWithMetadata returns an UploadFunc which copies all
// data from src reader into GCS and also sets the provided metadata
// fields onto the object.
func DataUploadWithMetadata(newReader ReaderFunc, metadata map[string]string) UploadFunc {
	return DataUploadWithOptions(newReader, pkgio.WriterOptions{Metadata: metadata})
}

// DataUploadWithOptions returns an UploadFunc which copies all data
// from src reader into GCS and also sets the provided attributes on
// the object.
func DataUploadWithOptions(newReader ReaderFunc, attrs pkgio.WriterOptions) UploadFunc {
	return func(writer dataWriter) (e error) {
		errors := make([]error, 0, 4)
		defer func() {
			if err := writer.Close(); err != nil {
				errors = append(errors, fmt.Errorf("writer close error: %w", err))
			}
			e = utilerrors.NewAggregate(errors)
		}()

		if writer.compressData() {
			var mediaType string
			path := writer.fullUploadPath()
			extIndex := strings.LastIndex(path, ".")
			if extIndex >= 0 {
				ext := path[extIndex:]
				mediaType = mime.TypeByExtension(ext)
			}
			if mediaType == "" {
				mediaType = "text/plain; charset=utf-8"
			}
			attrs.ContentType = &mediaType
			ce := "gzip"
			attrs.ContentEncoding = &ce
		}
		writer.ApplyWriterOptions(attrs)

		reader, err := newReader()
		if err != nil {
			errors = append(errors, fmt.Errorf("reader new error: %w", err))
			return e
		}
		defer func() {
			if err := reader.Close(); err != nil {
				errors = append(errors, fmt.Errorf("reader close error: %w", err))
			}
		}()

		if _, err := io.Copy(writer, reader); err != nil {
			errors = append(errors, fmt.Errorf("copy error: %w", err))
		}

		return e
	}
}

type dataWriter interface {
	io.WriteCloser
	fullUploadPath() string
	ApplyWriterOptions(opts pkgio.WriterOptions)
	compressData() bool
}

type openerObjectWriter struct {
	pkgio.Opener
	Context       context.Context
	Bucket        string
	Dest          string
	compress      bool
	opts          []pkgio.WriterOptions
	storageWriter pkgio.WriteCloser
	zipWriter     pkgio.WriteCloser
}

func (w *openerObjectWriter) Write(p []byte) (n int, err error) {
	if w.storageWriter == nil {
		w.storageWriter, err = w.Opener.Writer(w.Context, w.fullUploadPath(), w.opts...)
		if err != nil {
			return 0, err
		}
		if w.compress {
			w.zipWriter = gzip.NewWriter(w.storageWriter)
		}
	}
	if w.compress {
		return w.zipWriter.Write(p)
	} else {
		return w.storageWriter.Write(p)
	}
}

func (w *openerObjectWriter) Close() error {
	if w.storageWriter == nil {
		// Always create a writer even if Write() was never called
		// otherwise empty files are never created, because Write() is
		// never called for them
		if _, err := w.Write([]byte("")); err != nil {
			return err
		}
	}

	var errs []error
	if w.compress {
		if err := w.zipWriter.Close(); err != nil {
			errs = append(errs, err)
		}
		w.zipWriter = nil
	}
	if err := w.storageWriter.Close(); err != nil {
		errs = append(errs, err)
	}
	w.storageWriter = nil
	return utilerrors.NewAggregate(errs)
}

func (w *openerObjectWriter) ApplyWriterOptions(opts pkgio.WriterOptions) {
	w.opts = append(w.opts, opts)
}

func (w *openerObjectWriter) fullUploadPath() string {
	return fmt.Sprintf("%s/%s", w.Bucket, w.Dest)
}

func (w *openerObjectWriter) compressData() bool {
	return w.compress
}
