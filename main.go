package main

import (
	"archive/zip"
	"database/sql"
	"encoding/json"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"golang.org/x/exp/slices"
)

type Config struct {
	DatabasePath string
	GameZipPath  string
	LegacyPath   string
	ExtrasPath   string
	CgiPath      string
	OutputPath   string
	ExtremeTags  []string
}

type InfoContainer struct {
	Platforms     []InfoEntry `json:"platforms"`
	PlatformsNSFW []InfoEntry `json:"platformsNsfw"`
	Other         []InfoEntry `json:"other"`
}

type InfoEntry struct {
	Name string `json:"name"`
	File string `json:"file"`
	Size int64  `json:"size"`
}

type OutputZip struct {
	Name    string
	Extreme bool
	Files   []string
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
		Platforms:     make([]InfoEntry, 0),
		PlatformsNSFW: make([]InfoEntry, 0),
		Other:         make([]InfoEntry, 0),
	}

	db, err := sql.Open("sqlite3", config.DatabasePath)
	if err != nil {
		log.Fatal(err)
	}

	platforms := make([]string, 0)

	platformRows, err := db.Query("SELECT platform FROM game GROUP BY platform")
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

	for _, platform := range platforms {
		log.Println("Fetching info about " + platform + " GameZIPs...")

		platformZip := OutputZip{
			Name:    platform,
			Extreme: false,
			Files:   make([]string, 0),
		}
		platformZipExtreme := OutputZip{
			Name:    platform,
			Extreme: true,
			Files:   make([]string, 0),
		}

		gameZipRows, err := db.Query("SELECT path, game.tagsStr FROM game_data JOIN game ON game_data.gameId = game.id AND game.platform = ?", platform)
		if err != nil {
			log.Fatal(err)
		}

		for gameZipRows.Next() {
			var gameZip string
			var tagsStr string

			err := gameZipRows.Scan(&gameZip, &tagsStr)
			if err != sql.ErrNoRows && err != nil {
				log.Fatal(err)
			}

			extreme := false
			for _, v := range strings.Split(tagsStr, "; ") {
				if slices.Contains(config.ExtremeTags, v) {
					extreme = true
					break
				}
			}

			if !extreme {
				platformZip.Files = append(platformZip.Files, gameZip)
			} else {
				platformZipExtreme.Files = append(platformZipExtreme.Files, gameZip)
			}
		}

		if len(platformZip.Files) > 0 {
			platformZips = append(platformZips, platformZip)
		}
		if len(platformZipExtreme.Files) > 0 {
			platformZips = append(platformZips, platformZipExtreme)
		}
	}

	db.Close()

	for _, platformZip := range platformZips {
		fileName := "Flashpoint_" + strings.ReplaceAll(platformZip.Name, " ", "_")
		if platformZip.Extreme {
			fileName += "_NSFW"
		}
		fileName += "_" + time.Now().Format("20060102") + ".zip"

		log.Println("Creating " + fileName + "...")

		zipFile, err := os.OpenFile(filepath.Join(config.OutputPath, fileName), os.O_CREATE|os.O_WRONLY, 0222)
		if err != nil {
			log.Fatal(err)
		}
		zipFile.Truncate(0)

		zipWriter := zip.NewWriter(zipFile)

		for _, file := range platformZip.Files {
			fileData, err := os.OpenFile(filepath.Join(config.GameZipPath, file), os.O_RDONLY, 0111)
			if err != nil {
				log.Println("Error: " + file + " does not exist")
				continue
			}
			fileWriter, err := zipWriter.Create(file)
			if err != nil {
				log.Fatal(err)
			}

			if _, err := io.Copy(fileWriter, fileData); err != nil {
				log.Fatal(err)
			}
		}

		zipWriter.Close()

		zipInfo, err := zipFile.Stat()
		if err != nil {
			log.Fatal(err)
		}

		infoEntry := InfoEntry{
			Name: platformZip.Name,
			File: fileName,
			Size: zipInfo.Size(),
		}

		if !platformZip.Extreme {
			infoContainer.Platforms = append(infoContainer.Platforms, infoEntry)
		} else {
			infoContainer.PlatformsNSFW = append(infoContainer.PlatformsNSFW, infoEntry)
		}

		zipFile.Close()
	}

	CreateZip("Legacy", config.LegacyPath)
	CreateZip("Extras", config.ExtrasPath)
	CreateZip("cgi-bin", config.CgiPath)

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

func CreateZip(name string, rootPath string) {
	fileName := "Flashpoint_" + name + "_" + time.Now().Format("20060102") + ".zip"
	log.Println("Creating " + fileName + "...")

	zipFile, err := os.OpenFile(filepath.Join(config.OutputPath, fileName), os.O_CREATE|os.O_WRONLY, 0222)
	if err != nil {
		log.Fatal(err)
	}
	zipFile.Truncate(0)

	zipWriter := zip.NewWriter(zipFile)

	if err := filepath.WalkDir(rootPath, func(filePath string, d fs.DirEntry, _ error) error {
		zipPath := strings.TrimLeft(strings.ReplaceAll(strings.TrimPrefix(filePath, rootPath), "\\", "/"), "/")

		if !d.IsDir() {
			fileData, err := os.OpenFile(filePath, os.O_RDONLY, 0111)
			if err != nil {
				return err
			}
			fileWriter, err := zipWriter.Create(zipPath)
			if err != nil {
				return err
			}

			if _, err := io.Copy(fileWriter, fileData); err != nil {
				return err
			}
		} else if zipPath != "" {
			if _, err := zipWriter.Create(zipPath + "/"); err != nil {
				return err
			}
		}

		return nil
	}); err != nil {
		log.Fatal(err)
	}

	zipWriter.Close()

	zipInfo, err := zipFile.Stat()
	if err != nil {
		log.Fatal(err)
	}

	infoContainer.Other = append(infoContainer.Other, InfoEntry{
		Name: name,
		File: fileName,
		Size: zipInfo.Size(),
	})

	zipFile.Close()
}
