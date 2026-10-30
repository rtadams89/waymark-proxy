package logviewer

import (
	"archive/zip"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"imuslab.com/zoraxy/mod/utils"
)

type ViewerOption struct {
	RootFolder string //The root folder to scan for log
}

type Viewer struct {
	option *ViewerOption
}

type LogFile struct {
	Title    string
	Filename string
	Fullpath string
	Filesize int64
	Modified int64 // last write (Unix seconds)
}

func NewLogViewer(option *ViewerOption) *Viewer {
	return &Viewer{option: option}
}

// HandleListLog lists all the log files in the log folder.
func (v *Viewer) HandleListLog(w http.ResponseWriter, r *http.Request) {
	logFiles := v.ListLogFiles(false)
	js, _ := json.Marshal(logFiles)
	utils.SendJSONResponse(w, string(js))
}

// HandleReadLog reads a log of a given category and filename.
func (v *Viewer) HandleReadLog(w http.ResponseWriter, r *http.Request) {
	filename, err := utils.GetPara(r, "file")
	if err != nil {
		utils.SendErrorResponse(w, "invalid filename given")
		return
	}

	filter, err := utils.GetPara(r, "filter")
	if err != nil {
		filter = ""
	}

	linesParam, err := utils.GetPara(r, "lines")
	if err != nil {
		linesParam = "all"
	}

	content, err := v.LoadLogFile(strings.TrimSpace(filepath.Base(filename)))
	if err != nil {
		utils.SendErrorResponse(w, err.Error())
		return
	}

	//If filter is given, only return lines that contains the filter string
	if filter != "" {
		lines := strings.Split(content, "\n")
		filteredLines := []string{}
		for _, line := range lines {
			switch filter {
			case "error":
				if strings.Contains(line, ":error]") {
					filteredLines = append(filteredLines, line)
				}
			case "request":
				if strings.Contains(line, "[router:") {
					filteredLines = append(filteredLines, line)
				}
			case "system":
				if strings.Contains(line, "[system:") {
					filteredLines = append(filteredLines, line)
				}
			case "all":
				filteredLines = append(filteredLines, line)
			default:
				if strings.Contains(line, filter) {
					filteredLines = append(filteredLines, line)
				}
			}
		}
		content = strings.Join(filteredLines, "\n")
	}

	// Apply lines limit after filtering
	if linesParam != "all" {
		if lineLimit, err := strconv.Atoi(linesParam); err == nil && lineLimit > 0 {
			allLines := strings.Split(content, "\n")
			if len(allLines) > lineLimit {
				// Keep only the last lineLimit lines
				allLines = allLines[len(allLines)-lineLimit:]
				content = strings.Join(allLines, "\n")
			}
		}
	}

	content = utils.SanitizeLogContent(content)

	utils.SendTextResponse(w, content)
}

func (v *Viewer) ListLogFiles(showFullpath bool) map[string][]*LogFile {
	result := map[string][]*LogFile{}
	filepath.WalkDir(v.option.RootFolder, func(path string, di fs.DirEntry, err error) error {
		if filepath.Ext(path) == ".log" || strings.HasSuffix(path, ".log.gz") {
			catergory := filepath.Base(filepath.Dir(path))
			logList, ok := result[catergory]
			if !ok {
				logList = []*LogFile{}
			}

			fullpath := filepath.ToSlash(path)
			if !showFullpath {
				fullpath = ""
			}

			st, err := os.Stat(path)
			if err != nil {
				return nil
			}

			filename := filepath.Base(path)
			filename = strings.TrimSuffix(filename, ".log")

			logList = append(logList, &LogFile{
				Title:    strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)),
				Filename: filename,
				Fullpath: fullpath,
				Filesize: st.Size(),
				Modified: st.ModTime().Unix(),
			})

			result[catergory] = logList
		}

		return nil
	})
	return result
}

// readLogFileContent reads a log file, handling both compressed (.gz) and uncompressed files
func (v *Viewer) readLogFileContent(filepath string) ([]byte, error) {
	file, err := os.Open(filepath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	// Check if file is compressed
	if strings.HasSuffix(filepath, ".gz") {
		gzipReader, err := gzip.NewReader(file)
		if err != nil {
			// Try zip reader for older logs that use zip compression despite .gz extension
			zipReader, err := zip.OpenReader(filepath)
			if err != nil {
				return nil, err
			}
			defer zipReader.Close()
			if len(zipReader.File) == 0 {
				return nil, errors.New("zip file is empty")
			}
			zipFile := zipReader.File[0]
			rc, err := zipFile.Open()
			if err != nil {
				return nil, err
			}
			defer rc.Close()
			return io.ReadAll(rc)

		}
		defer gzipReader.Close()

		return io.ReadAll(gzipReader)
	}

	// Regular file
	return io.ReadAll(file)
}

func (v *Viewer) senatizeLogFilenameInput(filename string) string {
	filename = strings.TrimSuffix(filename, ".log.gz")
	filename = strings.TrimSuffix(filename, ".log")
	filename = filepath.ToSlash(filename)
	filename = filepath.Clean(filename)
	if strings.Contains(filename, "..") {
		return ""
	}
	//Check if .log.gz or .log exists
	if utils.FileExists(filepath.Join(v.option.RootFolder, filename+".log")) {
		return filepath.Join(v.option.RootFolder, filename+".log")
	}
	if utils.FileExists(filepath.Join(v.option.RootFolder, filename+".log.gz")) {
		return filepath.Join(v.option.RootFolder, filename+".log.gz")
	}
	return filepath.Join(v.option.RootFolder, filename)
}

func (v *Viewer) LoadLogFile(filename string) (string, error) {
	// filename may have no extension, .log or .log.gz
	logFilepath := v.senatizeLogFilenameInput(filename)
	if utils.FileExists(logFilepath) {
		content, err := v.readLogFileContent(logFilepath)
		if err != nil {
			return "", err
		}

		return string(content), nil
	}

	//Also check .log.gz
	logFilepathGz := logFilepath + ".gz"
	if utils.FileExists(logFilepathGz) {
		content, err := v.readLogFileContent(logFilepathGz)
		if err != nil {
			return "", err
		}

		return string(content), nil
	} else {
		return "", errors.New("log file not found")
	}
}
