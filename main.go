package main

import (
	"archive/zip"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"io/fs"
	"log"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"golang.org/x/exp/slices"
)

type Config struct {
	SourcePaths  PathConfig
	ZippedPaths  PathConfig
	DatabasePath string
	OutputPath   string
	ExtremeTags  []string
}

type PathConfig struct {
	GameZipPath string
	ImagePath   string
	LegacyPath  string
	ExtrasPath  string
	CgiPath     string
}

type InfoContainer struct {
	CompressedSize     int64       `json:"compressedSize"`
	UncompressedSize   int64       `json:"uncompressedSize"`
	Platforms          []InfoEntry `json:"platforms"`
	PlatformsNSFW      []InfoEntry `json:"platformsNsfw"`
	PlatformImages     []InfoEntry `json:"platformImages"`
	PlatformImagesNSFW []InfoEntry `json:"platformImagesNsfw"`
	Other              []InfoEntry `json:"other"`
}

type InfoEntry struct {
	Name string `json:"name"`
	File string `json:"file"`
	Size int64  `json:"size"`
	Hash string `json:"hash"`
}

type OutputZip struct {
	Name   string
	Suffix string
	Files  []string
}

var config Config
var infoContainer InfoContainer

func main() {
	if configRaw, err := os.ReadFile("config.json"); err != nil {
		log.Fatal(err)
	} else if err := json.Unmarshal([]byte(configRaw), &config); err != nil {
		log.Fatal(err)
	}

	infoContainer = InfoContainer{
		CompressedSize:     0,
		UncompressedSize:   0,
		Platforms:          make([]InfoEntry, 0),
		PlatformsNSFW:      make([]InfoEntry, 0),
		PlatformImages:     make([]InfoEntry, 0),
		PlatformImagesNSFW: make([]InfoEntry, 0),
		Other:              make([]InfoEntry, 0),
	}

	db, err := sql.Open("sqlite3", config.DatabasePath)
	if err != nil {
		log.Fatal(err)
	}

	platforms := make([]string, 0)

	platformRows, err := db.Query("SELECT name FROM platform_alias")
	if err != nil {
		log.Fatal(err)
	}

	for platformRows.Next() {
		var platform string

		err := platformRows.Scan(&platform)
		if err != sql.ErrNoRows && err != nil {
			log.Fatal(err)
		}

		platforms = append(platforms, platform)
	}

	platformZips := make([]OutputZip, 0)
	platformImageZips := make([]OutputZip, 0)

	for _, platform := range platforms {
		log.Println("Fetching info about " + platform + " entries...")

		platformZip, platformZipExtreme := OutputZip{platform, "", make([]string, 0)}, OutputZip{platform, "_NSFW", make([]string, 0)}

		gameZipRows, err := db.Query("SELECT path, game.tagsStr, game.platformsStr FROM game_data JOIN game ON game_data.gameId = game.id AND game.platformsStr LIKE ?", "%"+platform+"%")
		if err != nil {
			log.Fatal(err)
		}

		for gameZipRows.Next() {
			var path string
			var tagsStr string
			var platformsStr string

			err := gameZipRows.Scan(&path, &tagsStr, &platformsStr)
			if err != sql.ErrNoRows && err != nil {
				log.Fatal(err)
			}

			if !slices.Contains(strings.Split(platformsStr, "; "), platform) {
				continue
			}

			if !IsExtreme(tagsStr) {
				platformZip.Files = append(platformZip.Files, filepath.Join(config.SourcePaths.GameZipPath, path))
			} else {
				platformZipExtreme.Files = append(platformZipExtreme.Files, filepath.Join(config.SourcePaths.GameZipPath, path))
			}
		}

		if len(platformZip.Files) > 0 {
			platformZips = append(platformZips, platformZip)
		}
		if len(platformZipExtreme.Files) > 0 {
			platformZips = append(platformZips, platformZipExtreme)
		}

		platformImageZip, platformImageZipExtreme := OutputZip{platform, "_Images", make([]string, 0)}, OutputZip{platform, "_Images_NSFW", make([]string, 0)}

		imageRows, err := db.Query("SELECT id, tagsStr, platformsStr FROM game WHERE platformsStr LIKE ?", "%"+platform+"%")
		if err != nil {
			log.Fatal(err)
		}

		for imageRows.Next() {
			var id string
			var tagsStr string
			var platformsStr string

			err := imageRows.Scan(&id, &tagsStr, &platformsStr)
			if err != sql.ErrNoRows && err != nil {
				log.Fatal(err)
			}

			if !slices.Contains(strings.Split(platformsStr, "; "), platform) {
				continue
			}

			path := filepath.Join(id[:2], id[2:4], id+".png")

			if !IsExtreme(tagsStr) {
				platformImageZip.Files = append(platformImageZip.Files,
					filepath.Join(config.SourcePaths.ImagePath, "Logos", path),
					filepath.Join(config.SourcePaths.ImagePath, "Screenshots", path),
				)
			} else {
				platformImageZipExtreme.Files = append(platformImageZipExtreme.Files,
					filepath.Join(config.SourcePaths.ImagePath, "Logos", path),
					filepath.Join(config.SourcePaths.ImagePath, "Screenshots", path),
				)
			}
		}

		if len(platformImageZip.Files) > 0 {
			platformImageZips = append(platformImageZips, platformImageZip)
		}
		if len(platformImageZipExtreme.Files) > 0 {
			platformImageZips = append(platformImageZips, platformImageZipExtreme)
		}
	}

	db.Close()

	for _, platformZip := range platformZips {
		if platformZip.Suffix != "_NSFW" {
			CreateZip(platformZip, config.SourcePaths.GameZipPath, config.ZippedPaths.GameZipPath, &infoContainer.Platforms)
		} else {
			CreateZip(platformZip, config.SourcePaths.GameZipPath, config.ZippedPaths.GameZipPath, &infoContainer.PlatformsNSFW)
		}
	}
	for _, platformImageZip := range platformImageZips {
		if platformImageZip.Suffix != "_Images_NSFW" {
			CreateZip(platformImageZip, config.SourcePaths.ImagePath, config.ZippedPaths.ImagePath, &infoContainer.PlatformImages)
		} else {
			CreateZip(platformImageZip, config.SourcePaths.ImagePath, config.ZippedPaths.ImagePath, &infoContainer.PlatformImagesNSFW)
		}
	}

	CreateZip(OutputZip{"Legacy", "", GetFileList(config.SourcePaths.LegacyPath)}, config.SourcePaths.LegacyPath, config.ZippedPaths.LegacyPath, &infoContainer.Other)
	CreateZip(OutputZip{"Extras", "", GetFileList(config.SourcePaths.ExtrasPath)}, config.SourcePaths.ExtrasPath, config.ZippedPaths.ExtrasPath, &infoContainer.Other)
	CreateZip(OutputZip{"cgi-bin", "", GetFileList(config.SourcePaths.CgiPath)}, config.SourcePaths.CgiPath, config.ZippedPaths.CgiPath, &infoContainer.Other)

	log.Println("Writing to info.json...")

	infoJson, err := json.MarshalIndent(infoContainer, "", "  ")
	if err != nil {
		log.Fatal(err)
	}
	infoFile, err := os.OpenFile(filepath.Join(config.OutputPath, "info.json"), os.O_CREATE|os.O_WRONLY, 0222)
	if err != nil {
		log.Fatal(err)
	}

	infoFile.Truncate(0)
	infoFile.Write(infoJson)
	infoFile.Close()

	log.Println("Done!")
}

func CreateZip(zipData OutputZip, sourcePath string, zippedPath string, outputList *[]InfoEntry) {
	zipFileName := "Flashpoint_" + strings.ReplaceAll(zipData.Name, " ", "_") + zipData.Suffix + "_" + time.Now().Format("20060102") + ".zip"
	log.Println("Creating " + zipFileName + "...")

	zipFile, err := os.OpenFile(filepath.Join(config.OutputPath, zipFileName), os.O_CREATE|os.O_WRONLY, 0222)
	if err != nil {
		log.Fatal(err)
	}
	zipFile.Truncate(0)

	zipWriter := zip.NewWriter(zipFile)

	for _, file := range zipData.Files {
		zippedFile := path.Join(zippedPath, filepath.ToSlash(strings.TrimPrefix(file, sourcePath)))
		if zippedFile == zippedPath {
			continue
		}

		if !strings.HasSuffix(file, string(os.PathSeparator)) {
			fileData, err := os.OpenFile(file, os.O_RDONLY, 0111)
			if err != nil {
				//log.Println("Error: " + file + " does not exist")
				continue
			}
			if fileInfo, err := fileData.Stat(); err == nil {
				infoContainer.UncompressedSize += fileInfo.Size()
			} else {
				log.Fatal(err)
			}

			fileWriter, err := zipWriter.Create(zippedFile)
			if err != nil {
				log.Fatal(err)
			}
			if _, err := io.Copy(fileWriter, fileData); err != nil {
				log.Fatal(err)
			}
		} else {
			if _, err := zipWriter.Create(zippedFile + "/"); err != nil {
				log.Fatal(err)
			}
		}
	}

	zipWriter.Close()
	zipFile.Close()

	// zipFile needs to be closed and re-opened for the hasher to work, even with os.O_RDONLY set
	// otherwise it returns the same hash every time
	zipFile, err = os.OpenFile(filepath.Join(config.OutputPath, zipFileName), os.O_RDONLY, 0111)
	if err != nil {
		log.Fatal(err)
	}

	zipInfo, err := zipFile.Stat()
	if err == nil {
		infoContainer.CompressedSize += zipInfo.Size()
	} else {
		log.Fatal(err)
	}
	hasher := sha256.New()
	if _, err := io.Copy(hasher, zipFile); err != nil {
		log.Fatal(err)
	}

	infoEntry := InfoEntry{
		Name: zipData.Name,
		File: zipFileName,
		Size: zipInfo.Size(),
		Hash: hex.EncodeToString(hasher.Sum(nil)),
	}

	*outputList = append(*outputList, infoEntry)

	zipFile.Close()
}

func GetFileList(rootPath string) []string {
	fileList := make([]string, 0)

	if err := filepath.WalkDir(rootPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if !d.IsDir() {
			fileList = append(fileList, path)
		} else {
			fileList = append(fileList, path+string(os.PathSeparator))
		}

		return nil
	}); err != nil {
		log.Println("Error: " + rootPath + " cannot be accessed")
	}

	return fileList
}

func IsExtreme(tagsStr string) bool {
	for _, tag := range strings.Split(tagsStr, "; ") {
		if slices.Contains(config.ExtremeTags, tag) {
			return true
		}
	}
	return false
}
