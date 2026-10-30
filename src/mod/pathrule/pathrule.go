package pathrule

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"imuslab.com/zoraxy/mod/utils"
)

// Advanced settings and rules for particular request paths

type Options struct {
	Enabled      bool   //If the pathrule is enabled.
	ConfigFolder string //The folder to store the path blocking config files
}

type BlockingPath struct {
	UUID          string
	MatchingPath  string
	ExactMatch    bool
	StatusCode    int
	CustomHeaders http.Header
	CustomHTML    []byte
	Enabled       bool
	CaseSenitive  bool
}

type Handler struct {
	Options       *Options
	BlockingPaths []*BlockingPath
}

// Create a new path blocker handler
func NewPathRuleHandler(options *Options) *Handler {
	if !utils.FileExists(options.ConfigFolder) {
		os.Mkdir(options.ConfigFolder, 0775)
	}

	return &Handler{
		Options:       options,
		BlockingPaths: []*BlockingPath{},
	}
}

// Get the blocker from matching path (path match, ignore tailing slash)
func (h *Handler) GetPathBlockerFromMatchingPath(matchingPath string) *BlockingPath {
	for _, blocker := range h.BlockingPaths {
		if blocker.MatchingPath == matchingPath {
			return blocker
		} else if strings.TrimSuffix(blocker.MatchingPath, "/") == strings.TrimSuffix(matchingPath, "/") {
			return blocker
		}
	}

	return nil
}

func (h *Handler) GetPathBlockerFromUUID(UUID string) *BlockingPath {
	for _, blocker := range h.BlockingPaths {
		if blocker.UUID == UUID {
			return blocker
		}
	}

	return nil
}

func (h *Handler) AddBlockingPath(pathBlocker *BlockingPath) error {
	blockerPath := pathBlocker.MatchingPath
	targetBlocker := h.GetPathBlockerFromMatchingPath(blockerPath)
	if targetBlocker != nil {
		return errors.New("path blocker with the same path already exists")
	}

	h.BlockingPaths = append(h.BlockingPaths, pathBlocker)

	return h.SaveBlockerToFile(pathBlocker)
}

func (h *Handler) RemoveBlockingPathByUUID(uuid string) error {
	newBlockingList := []*BlockingPath{}
	for _, thisBlocker := range h.BlockingPaths {
		if thisBlocker.UUID != uuid {
			newBlockingList = append(newBlockingList, thisBlocker)
		}
	}

	if len(h.BlockingPaths) == len(newBlockingList) {
		return errors.New("given matching path blocker not exists")
	}

	h.BlockingPaths = newBlockingList

	return h.RemoveBlockerFromFile(uuid)
}

// blockerFilename returns the config file of a blocker, accepting only a
// well-formed UUID as the name.
func (h *Handler) blockerFilename(id string) (string, error) {
	parsed, err := uuid.Parse(id)
	if err != nil {
		return "", errors.New("invalid blocker UUID")
	}
	return filepath.Join(h.Options.ConfigFolder, parsed.String()), nil
}

func (h *Handler) SaveBlockerToFile(pathBlocker *BlockingPath) error {
	saveFilename, err := h.blockerFilename(pathBlocker.UUID)
	if err != nil {
		return err
	}
	js, _ := json.MarshalIndent(pathBlocker, "", " ")
	return utils.WriteFileAtomic(saveFilename, js, 0640)
}

func (h *Handler) RemoveBlockerFromFile(id string) error {
	expectedConfigFile, err := h.blockerFilename(id)
	if err != nil {
		return err
	}
	if !utils.FileExists(expectedConfigFile) {
		return errors.New("config file not found on disk")
	}

	return os.Remove(expectedConfigFile)
}
