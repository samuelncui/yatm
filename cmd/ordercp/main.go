package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/abc950309/tapewriter/mmap"
	"github.com/schollz/progressbar/v3"
	"github.com/sirupsen/logrus"
)

const (
	unexpectFileMode = os.ModeType &^ os.ModeDir
	batchSize        = 1024 * 1024
)

func main() {
	src, dst := os.Args[1], os.Args[2]
	c, err := NewCopyer(dst, src)
	if err != nil {
		panic(err)
	}

	c.Run()
}

type Copyer struct {
	bar        *progressbar.ProgressBar
	dst, src   string
	fromTape   bool
	num        int64
	files      []*Job
	errs       []error
	copyPipe   chan *CopyJob
	changePipe chan *Job
}

func NewCopyer(dst, src string) (*Copyer, error) {
	dst, src = strings.TrimSpace(dst), strings.TrimSpace(src)
	if dst == "" {
		return nil, fmt.Errorf("dst not found")
	}
	if src == "" {
		return nil, fmt.Errorf("src not found")
	}
	if dst[len(dst)-1] != '/' {
		dst = dst + "/"
	}

	dstStat, err := os.Stat(dst)
	if err != nil {
		return nil, fmt.Errorf("dst path '%s', %w", dst, err)
	}
	if !dstStat.IsDir() {
		return nil, fmt.Errorf("dst path is not a dir")
	}

	srcStat, err := os.Stat(src)
	if err != nil {
		return nil, fmt.Errorf("src path '%s', %w", src, err)
	}
	if srcStat.IsDir() && src[len(src)-1] != '/' {
		src = src + "/"
	}

	c := &Copyer{
		dst: dst, src: src,
		copyPipe:   make(chan *CopyJob, 32),
		changePipe: make(chan *Job, 8),
	}
	c.walk("", true)

	var total int64
	for _, file := range c.files {
		total += file.Size
	}
	c.bar = progressbar.DefaultBytes(total)

	return c, nil
}

func (c *Copyer) Run() {
	ctx, cancel := context.WithCancel(context.Background())

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
			if err := c.copy(ctx, copyer); err != nil {
				c.ReportError(c.dst+copyer.Path, err)
				if err := os.Remove(c.dst + copyer.Path); err != nil {
					c.ReportError(c.dst+copyer.Path, fmt.Errorf("delete file with error fail, %w", err))
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

func (c *Copyer) walk(path string, first bool) {
	stat, err := os.Stat(c.src + path)
	if err != nil {
		c.ReportError(c.src+path, fmt.Errorf("walk get stat, %w", err))
		return
	}

	job := NewJobFromFileInfo(path, stat)
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
		files, err := os.ReadDir(c.src + path)
		if err != nil {
			c.ReportError(c.src+path, fmt.Errorf("walk read dir, %w", err))
			return
		}

		for _, file := range files {
			c.walk(file.Name(), false)
		}
		return
	}

	enterJob := new(Job)
	*enterJob = *job
	enterJob.Type = JobTypeEnterDir
	c.files = append(c.files, enterJob)

	files, err := os.ReadDir(c.src + path)
	if err != nil {
		c.ReportError(c.src+path, fmt.Errorf("walk read dir, %w", err))
		return
	}

	for _, file := range files {
		if first {
			c.walk(file.Name(), false)
			continue
		}
		c.walk(path+"/"+file.Name(), false)
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

	name := c.src + job.Path
	file, err := mmap.Open(name)
	if err != nil {
		c.ReportError(name, fmt.Errorf("open src file fail, %w", err))
		return
	}

	c.copyPipe <- &CopyJob{Job: job, src: file}
}

func (c *Copyer) copy(ctx context.Context, job *CopyJob) error {
	if job.src == nil {
		c.changePipe <- job.Job
		return nil
	}
	defer job.src.Close()

	name := c.dst + job.Path
	file, err := os.Create(name)
	if err != nil {
		return fmt.Errorf("open dst file fail, %w", err)
	}
	defer file.Close()

	c.bar.Describe(fmt.Sprintf("[%d/%d]: %s", job.Number, c.num, job.Path))
	if err := c.streamCopy(ctx, file, job.src); err != nil {
		return fmt.Errorf("copy file fail, %w", err)
	}

	c.changePipe <- job.Job
	return nil
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

func (c *Copyer) streamCopy(ctx context.Context, dst io.Writer, src *mmap.ReaderAt) error {
	for idx := int64(0); ; idx += batchSize {
		buf, err := src.Slice(idx, batchSize)
		if err != nil {
			return fmt.Errorf("slice mmap fail, %w", err)
		}
		nr := len(buf)

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

		c.bar.Add(nr)
		if len(buf) < batchSize {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
	}
}

type JobType uint8

const (
	JobTypeNormal = JobType(iota)
	JobTypeEnterDir
	JobTypeExitDir
)

type Job struct {
	Path    string
	Type    JobType
	Number  int64
	Name    string      // base name of the file
	Size    int64       // length in bytes for regular files; system-dependent for others
	Mode    os.FileMode // file mode bits
	ModTime time.Time   // modification time
}

func NewJobFromFileInfo(path string, info os.FileInfo) *Job {
	job := &Job{
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
