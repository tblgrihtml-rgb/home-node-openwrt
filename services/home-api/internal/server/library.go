package server

import (
	"io/fs"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"homenode/services/home-api/internal/aniliberty"
	"homenode/services/home-api/internal/store"
)

type LibraryTitle struct {
	Title      string        `json:"title"`
	Category   string        `json:"category"`
	Folder     string        `json:"folder"`
	FileCount  int           `json:"file_count"`
	Size       int64         `json:"size"`
	ModifiedAt string        `json:"modified_at"`
	PosterURL  string        `json:"poster_url"`
	Artist     string        `json:"artist,omitempty"`
	SMBPath    string        `json:"smb_path"`
	Files      []LibraryFile `json:"files"`
}

type LibraryFile struct {
	Path       string `json:"path"`
	Name       string `json:"name"`
	Size       int64  `json:"size"`
	ModifiedAt string `json:"modified_at"`
}

func (s *Server) library(w http.ResponseWriter, r *http.Request) {
	grouped := make(map[string]*LibraryTitle)
	err := filepath.WalkDir(s.cfg.MediaRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if !isSupportedMediaExtension(ext) {
			return nil
		}
		relative, err := filepath.Rel(s.cfg.MediaRoot, path)
		if err != nil {
			return err
		}
		parts := strings.Split(filepath.ToSlash(relative), "/")
		if len(parts) < 2 {
			return nil
		}
		category := parts[0]
		title, artist := "Без папки", ""
		key := category + "/" + title
		if category == "music" && len(parts) >= 4 {
			artist, title = parts[1], parts[2]
			key = strings.Join(parts[:3], "/")
		} else if len(parts) >= 3 {
			title = parts[1]
			key = strings.Join(parts[:2], "/")
		}
		item := grouped[key]
		if item == nil {
			item = &LibraryTitle{
				Title: title, Artist: artist, Category: category, Folder: key,
				SMBPath: "smb://192.168.1.1/HomeNode/media/" + key,
			}
			grouped[key] = item
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		item.FileCount++
		item.Size += info.Size()
		modified := info.ModTime().UTC().Format(time.RFC3339)
		item.Files = append(item.Files, LibraryFile{
			Path: filepath.ToSlash(relative), Name: entry.Name(), Size: info.Size(), ModifiedAt: modified,
		})
		if modified > item.ModifiedAt {
			item.ModifiedAt = modified
		}
		return nil
	})
	if err != nil {
		s.logger.Error("scan library", "error", err)
		writeError(w, http.StatusInternalServerError, "Не удалось прочитать медиатеку")
		return
	}

	items := make([]LibraryTitle, 0, len(grouped))
	for _, item := range grouped {
		sort.Slice(item.Files, func(i, j int) bool {
			return strings.ToLower(item.Files[i].Name) < strings.ToLower(item.Files[j].Name)
		})
		s.enrichLibraryTitle(r, item)
		items = append(items, *item)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].ModifiedAt == items[j].ModifiedAt {
			return items[i].Title < items[j].Title
		}
		return items[i].ModifiedAt > items[j].ModifiedAt
	})
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) enrichLibraryTitle(r *http.Request, item *LibraryTitle) {
	if item.Category != "anime" || item.Title == "Без папки" {
		return
	}
	cached, ok, err := s.store.MediaTitle(r.Context(), item.Folder)
	if err == nil && ok {
		item.PosterURL = cached.PosterURL
		if strings.Contains(cached.PosterURL, "quality=full") {
			return
		}
	}
	releases, err := s.anime.Search(r.Context(), item.Title)
	if err != nil || len(releases) == 0 {
		return
	}
	release := bestReleaseMatch(releases, item.Title)
	poster := release.Poster.Optimized.Src
	if poster == "" {
		poster = release.Poster.Optimized.Preview
	}
	if poster == "" {
		poster = release.Poster.Src
	}
	if poster == "" {
		poster = release.Poster.Preview
	}
	if poster == "" {
		poster = release.Poster.Optimized.Thumbnail
	}
	if poster == "" {
		poster = release.Poster.Thumbnail
	}
	if poster != "" && !strings.HasPrefix(poster, "http") {
		poster = "https://aniliberty.top" + poster
	}
	if poster != "" {
		poster += "?quality=full"
	}
	item.PosterURL = poster
	_ = s.store.UpsertMediaTitle(r.Context(), store.MediaTitle{
		Folder: item.Folder, Title: item.Title, PosterURL: poster,
		Source: "aniliberty", ReleaseID: release.ID,
	})
}

func bestReleaseMatch(releases []aniliberty.Release, title string) aniliberty.Release {
	for _, release := range releases {
		if strings.EqualFold(strings.TrimSpace(release.Name.Main), strings.TrimSpace(title)) {
			return release
		}
	}
	return releases[0]
}
