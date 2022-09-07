package main

import (
	"context"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/abc950309/tapewriter/library"
	"github.com/abc950309/tapewriter/mmap"
	"github.com/minio/sha256-simd"
	"github.com/schollz/progressbar/v3"
	"github.com/sirupsen/logrus"
)

const (
	unexpectFileMode = os.ModeType &^ os.ModeDir
	batchSize        = 1024 * 1024
)

var (
	shaPool = &sync.Pool{New: func() interface{} { return sha256.New() }}
)

func main() {
	src, dst := os.Args[1], os.Args[2]
	c, err := NewCopyer(dst, src)
	if err != nil {
		panic(err)
	}
	c.Run()

	if p := os.Getenv("ORDERCP_REPORT_PATH"); p != "" {
		errs := make([]string, 0, len(c.errs))
		for _, e := range c.errs {
			errs = append(errs, e.Error())
		}
		report, _ := json.Marshal(map[string]interface{}{"errors": errs, "files": c.results})

		n := os.Getenv("ORDERCP_REPORT_FILENAME")
		if n == "" {
			n = time.Now().Format("2006-01-02T15:04:05.999999.csv")
		}

		r, err := os.Create(fmt.Sprintf("%s/%s", p, n))
		if err != nil {
			logrus.Warnf("open report fail, path= '%s', err= %w", fmt.Sprintf("%s/%s", p, n), err)
			logrus.Infof("report: %s", report)
			return
		}
		defer r.Close()

		r.Write(report)
	}
}

type Copyer struct {
	bar        *progressbar.ProgressBar
	src        []string
	dst        string
	copyed     int64
	num        int64
	files      []*Job
	errs       []error
	copyPipe   chan *CopyJob
	changePipe chan *Job

	results []*library.TapeFile
}

func NewCopyer(dst string, src ...string) (*Copyer, error) {
	dst = strings.TrimSpace(dst)
	if dst == "" {
		return nil, fmt.Errorf("dst not found")
	}
	if dst[len(dst)-1] != '/' {
		dst = dst + "/"
	}

	filtered := make([]string, 0, len(src))
	for _, s := range src {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}

		srcStat, err := os.Stat(s)
		if err != nil {
			return nil, fmt.Errorf("check src path '%s', %w", src, err)
		}
		if srcStat.IsDir() && s[len(s)-1] != '/' {
			s = s + "/"
		}

		filtered = append(filtered, s)
	}
	if len(filtered) == 0 {
		return nil, fmt.Errorf("src not found")
	}
	src = filtered

	dstStat, err := os.Stat(dst)
	if err != nil {
		return nil, fmt.Errorf("check dst path '%s', %w", dst, err)
	}
	if !dstStat.IsDir() {
		return nil, fmt.Errorf("dst path is not a dir")
	}

	c := &Copyer{
		dst: dst, src: src,
		copyPipe:   make(chan *CopyJob, 32),
		changePipe: make(chan *Job, 8),
	}
	for _, s := range c.src {
		c.walk(s, "", true)
	}

	var total int64
	for _, file := range c.files {
		total += file.Size
	}
	c.bar = progressbar.DefaultBytes(total)

	return c, nil
}

func (c *Copyer) Run() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt)
	go func() {
		for sig := range signals {
			if sig != os.Interrupt {
				continue
			}
			cancel()
		}
	}()

	go func() {
		ticker := time.NewTicker(time.Millisecond * 500)
		defer ticker.Stop()

		last := int64(0)
		for range ticker.C {
			current := atomic.LoadInt64(&c.copyed)
			c.bar.Add(int(current - last))
			last = current

			select {
			case <-ctx.Done():
				close(c.copyPipe)
				return
			default:
			}
		}
	}()

	go func() {
		for _, file := range c.files {
			c.prepare(ctx, file)

			select {
			case <-ctx.Done():
				close(c.copyPipe)
				return
			default:
			}
		}
		close(c.copyPipe)
	}()

	go func() {
		for copyer := range c.copyPipe {
			hash, err := c.copy(ctx, copyer)
			if err != nil {
				c.ReportError(c.dst+copyer.Path, err)
				if err := os.Remove(c.dst + copyer.Path); err != nil {
					c.ReportError(c.dst+copyer.Path, fmt.Errorf("delete file with error fail, %w", err))
				}
			} else {
				if !copyer.Mode.IsDir() {
					c.results = append(c.results, &library.TapeFile{
						Path:      copyer.Path,
						Size:      copyer.Size,
						Mode:      copyer.Mode,
						ModTime:   copyer.ModTime,
						WriteTime: time.Now(),
						Hash:      hash,
					})
				}
			}

			select {
			case <-ctx.Done():
				close(c.changePipe)
				return
			default:
			}
		}
		close(c.changePipe)
	}()

	for file := range c.changePipe {
		c.changeInfo(file)
	}
}

func (c *Copyer) ReportError(file string, err error) {
	logrus.Errorf("'%s', %s", file, err)
	c.errs = append(c.errs, fmt.Errorf("'%s': %w", file, err))
}

func (c *Copyer) walk(src, path string, first bool) {
	name := src + path

	stat, err := os.Stat(name)
	if err != nil {
		c.ReportError(name, fmt.Errorf("walk get stat, %w", err))
		return
	}

	job := NewJobFromFileInfo(src, path, stat)
	if job.Mode&unexpectFileMode != 0 {
		return
	}

	if !job.Mode.IsDir() {
		c.num++
		job.Number = c.num
		c.files = append(c.files, job)
		return
	}
	if first {
		files, err := os.ReadDir(name)
		if err != nil {
			c.ReportError(name, fmt.Errorf("walk read dir, %w", err))
			return
		}

		for _, file := range files {
			c.walk(src, file.Name(), false)
		}
		return
	}

	enterJob := new(Job)
	*enterJob = *job
	enterJob.Type = JobTypeEnterDir
	c.files = append(c.files, enterJob)

	files, err := os.ReadDir(name)
	if err != nil {
		c.ReportError(name, fmt.Errorf("walk read dir, %w", err))
		return
	}

	for _, file := range files {
		c.walk(src, path+"/"+file.Name(), false)
	}

	exitJob := new(Job)
	*exitJob = *job
	exitJob.Type = JobTypeExitDir
	c.files = append(c.files, exitJob)
}

func (c *Copyer) prepare(ctx context.Context, job *Job) {
	switch job.Type {
	case JobTypeEnterDir:
		name := c.dst + job.Path
		err := os.Mkdir(name, job.Mode&os.ModePerm)
		if err != nil {
			c.ReportError(name, fmt.Errorf("mkdir fail, %w", err))
			return
		}
		return
	case JobTypeExitDir:
		c.copyPipe <- &CopyJob{Job: job}
		return
	}

	name := job.Source + job.Path
	file, err := mmap.Open(name)
	if err != nil {
		c.ReportError(name, fmt.Errorf("open src file fail, %w", err))
		return
	}

	c.copyPipe <- &CopyJob{Job: job, src: file}
}

func (c *Copyer) copy(ctx context.Context, job *CopyJob) ([]byte, error) {
	if job.src == nil {
		c.changePipe <- job.Job
		return nil, nil
	}
	defer job.src.Close()

	name := c.dst + job.Path
	file, err := os.Create(name)
	if err != nil {
		return nil, fmt.Errorf("open dst file fail, %w", err)
	}
	defer file.Close()

	c.bar.Describe(fmt.Sprintf("[%d/%d]: %s", job.Number, c.num, job.Path))
	hash, err := c.streamCopy(ctx, file, job.src)
	if err != nil {
		return nil, fmt.Errorf("copy file fail, %w", err)
	}

	c.changePipe <- job.Job
	return hash, nil
}

func (c *Copyer) changeInfo(info *Job) {
	name := c.dst + info.Path

	if err := os.Chmod(name, info.Mode&os.ModePerm); err != nil {
		c.ReportError(name, fmt.Errorf("change info, chmod fail, %w", err))
	}
	if err := os.Chtimes(name, info.ModTime, info.ModTime); err != nil {
		c.ReportError(name, fmt.Errorf("change info, chtimes fail, %w", err))
	}
}

func (c *Copyer) streamCopy(ctx context.Context, dst io.Writer, src *mmap.ReaderAt) (h []byte, err error) {
	if src.Len() == 0 {
		return nil, nil
	}

	sha := shaPool.Get().(hash.Hash)
	sha.Reset()
	defer shaPool.Put(sha)

	var wg sync.WaitGroup
	hashChan := make(chan []byte, 4)
	defer func() {
		close(hashChan)
		if err != nil {
			return
		}

		wg.Wait()
		h = sha.Sum(nil)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		for buf := range hashChan {
			sha.Write(buf)
		}
	}()

	err = func() error {
		for idx := int64(0); ; idx += batchSize {
			buf, err := src.Slice(idx, batchSize)
			if err != nil {
				return fmt.Errorf("slice mmap fail, %w", err)
			}
			nr := len(buf)
			hashChan <- buf

			nw, ew := dst.Write(buf)
			if nw < 0 || nr < nw {
				nw = 0
				if ew == nil {
					return fmt.Errorf("write fail, unexpected return, byte_num= %d", nw)
				}
				return fmt.Errorf("write fail, %w", ew)
			}
			if nr != nw {
				return fmt.Errorf("write fail, write and read bytes not equal, read= %d write= %d", nr, nw)
			}

			atomic.AddInt64(&c.copyed, int64(nr))
			if len(buf) < batchSize {
				return nil
			}

			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
		}
	}()
	return
}

type JobType uint8

const (
	JobTypeNormal = JobType(iota)
	JobTypeEnterDir
	JobTypeExitDir
)

type Job struct {
	Source  string
	Path    string
	Type    JobType
	Number  int64
	Name    string      // base name of the file
	Size    int64       // length in bytes for regular files; system-dependent for others
	Mode    os.FileMode // file mode bits
	ModTime time.Time   // modification time
}

func NewJobFromFileInfo(src, path string, info os.FileInfo) *Job {
	job := &Job{
		Source:  src,
		Path:    path,
		Name:    info.Name(),
		Size:    info.Size(),
		Mode:    info.Mode(),
		ModTime: info.ModTime(),
	}
	return job
}

type CopyJob struct {
	*Job
	src *mmap.ReaderAt
}
