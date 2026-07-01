package handler

import (
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/datarhei/core/v16/http/api"
	"github.com/datarhei/core/v16/http/fs"
	"github.com/datarhei/core/v16/http/handler/util"

	"github.com/labstack/echo/v4"
)

// The FSHandler type provides handlers for manipulating a filesystem
type FSHandler struct {
	fs fs.FS
}

// NewFS return a new FSHandler type. You have to provide a filesystem to act on.
func NewFS(fs fs.FS) *FSHandler {
	return &FSHandler{
		fs: fs,
	}
}

func (h *FSHandler) GetFile(c echo.Context) error {
	path := util.PathWildcardParam(c)

	mimeType := c.Response().Header().Get(echo.HeaderContentType)
	c.Response().Header().Del(echo.HeaderContentType)

	file := h.fs.Filesystem.Open(path)
	if file == nil {
		return api.Err(http.StatusNotFound, "File not found", path)
	}

	stat, _ := file.Stat()

	if len(h.fs.DefaultFile) != 0 {
		if stat.IsDir() {
			path = filepath.Join(path, h.fs.DefaultFile)

			file.Close()

			file = h.fs.Filesystem.Open(path)
			if file == nil {
				return api.Err(http.StatusNotFound, "File not found", path)
			}

			stat, _ = file.Stat()
		}
	}

	defer file.Close()

	c.Response().Header().Set("Last-Modified", stat.ModTime().UTC().Format("Mon, 02 Jan 2006 15:04:05 GMT"))

	if path, ok := stat.IsLink(); ok {
		path = filepath.Clean("/" + path)

		if path[0] == '/' {
			path = path[1:]
		}

		return c.Redirect(http.StatusMovedPermanently, path)
	}

	c.Response().Header().Set(echo.HeaderContentType, mimeType)
	c.Response().Header().Set("Accept-Ranges", "bytes")

	if c.Request().Method == "HEAD" {
		return c.Blob(http.StatusOK, "application/data", nil)
	}

	if rangeHeader := c.Request().Header.Get("Range"); len(rangeHeader) != 0 {
		if seeker, ok := file.(io.Seeker); ok {
			if start, end, ok := parseRange(rangeHeader, stat.Size()); ok {
				if _, err := seeker.Seek(start, io.SeekStart); err == nil {
					length := end - start + 1

					c.Response().Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, stat.Size()))
					c.Response().Header().Set(echo.HeaderContentLength, strconv.FormatInt(length, 10))

					return c.Stream(http.StatusPartialContent, "application/data", io.LimitReader(file, length))
				}
			}
		}
	}

	return c.Stream(http.StatusOK, "application/data", file)
}

// parseRange parses a single-range HTTP Range header (e.g. "bytes=0-1023" or
// "bytes=1024-") against a known file size. It doesn't support multi-range
// requests (e.g. "bytes=0-10,20-30"), which is a common, well-understood
// limitation shared by many minimal Range implementations.
func parseRange(header string, size int64) (start, end int64, ok bool) {
	const prefix = "bytes="

	if !strings.HasPrefix(header, prefix) {
		return 0, 0, false
	}

	spec := strings.TrimPrefix(header, prefix)
	if strings.Contains(spec, ",") {
		return 0, 0, false
	}

	parts := strings.SplitN(spec, "-", 2)
	if len(parts) != 2 {
		return 0, 0, false
	}

	if len(parts[0]) == 0 {
		// Suffix range, e.g. "-500" means the last 500 bytes
		suffix, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil || suffix <= 0 {
			return 0, 0, false
		}

		if suffix > size {
			suffix = size
		}

		return size - suffix, size - 1, true
	}

	start, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || start < 0 || start >= size {
		return 0, 0, false
	}

	if len(parts[1]) == 0 {
		return start, size - 1, true
	}

	end, err = strconv.ParseInt(parts[1], 10, 64)
	if err != nil || end < start {
		return 0, 0, false
	}

	if end >= size {
		end = size - 1
	}

	return start, end, true
}

func (h *FSHandler) PutFile(c echo.Context) error {
	path := util.PathWildcardParam(c)

	c.Response().Header().Del(echo.HeaderContentType)

	req := c.Request()

	_, created, err := h.fs.Filesystem.WriteFileReader(path, req.Body)
	if err != nil {
		return api.Err(http.StatusBadRequest, "Bad request", "%s", err)
	}

	if h.fs.Cache != nil {
		h.fs.Cache.Delete(path)

		if len(h.fs.DefaultFile) != 0 {
			if strings.HasSuffix(path, "/"+h.fs.DefaultFile) {
				path := strings.TrimSuffix(path, h.fs.DefaultFile)
				h.fs.Cache.Delete(path)
			}
		}
	}

	c.Response().Header().Set("Content-Location", req.URL.RequestURI())

	if created {
		return c.String(http.StatusCreated, "")
	}

	return c.NoContent(http.StatusNoContent)
}

func (h *FSHandler) DeleteFile(c echo.Context) error {
	path := util.PathWildcardParam(c)

	c.Response().Header().Del(echo.HeaderContentType)

	size := h.fs.Filesystem.Remove(path)

	if h.fs.Cache != nil {
		h.fs.Cache.Delete(path)

		if len(h.fs.DefaultFile) != 0 {
			if strings.HasSuffix(path, "/"+h.fs.DefaultFile) {
				path := strings.TrimSuffix(path, h.fs.DefaultFile)
				h.fs.Cache.Delete(path)
			}
		}
	}

	if size < 0 {
		return api.Err(http.StatusNotFound, "File not found", path)
	}

	return c.String(http.StatusOK, "Deleted: "+path)
}

func (h *FSHandler) ListFiles(c echo.Context) error {
	pattern := util.DefaultQuery(c, "glob", "")
	sortby := util.DefaultQuery(c, "sort", "none")
	order := util.DefaultQuery(c, "order", "asc")

	files := h.fs.Filesystem.List("/", pattern)

	var sortFunc func(i, j int) bool

	switch sortby {
	case "name":
		if order == "desc" {
			sortFunc = func(i, j int) bool { return files[i].Name() > files[j].Name() }
		} else {
			sortFunc = func(i, j int) bool { return files[i].Name() < files[j].Name() }
		}
	case "size":
		if order == "desc" {
			sortFunc = func(i, j int) bool { return files[i].Size() > files[j].Size() }
		} else {
			sortFunc = func(i, j int) bool { return files[i].Size() < files[j].Size() }
		}
	default:
		if order == "asc" {
			sortFunc = func(i, j int) bool { return files[i].ModTime().Before(files[j].ModTime()) }
		} else {
			sortFunc = func(i, j int) bool { return files[i].ModTime().After(files[j].ModTime()) }
		}
	}

	sort.Slice(files, sortFunc)

	var fileinfos []api.FileInfo = make([]api.FileInfo, len(files))

	for i, f := range files {
		fileinfos[i] = api.FileInfo{
			Name:    f.Name(),
			Size:    f.Size(),
			LastMod: f.ModTime().Unix(),
		}
	}

	return c.JSON(http.StatusOK, fileinfos)
}
