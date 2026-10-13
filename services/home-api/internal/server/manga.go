package server

import (
	"archive/zip"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"homenode/services/home-api/internal/store"
)

const (
	maxMangaBooks       = 200
	maxEPUBEntries      = 100000
	maxEPUBPackageBytes = 8 << 20
	maxEPUBPageBytes    = 32 << 20
)

var imageAttributePattern = regexp.MustCompile(`(?i)(?:src|href|xlink:href|data-src)\s*=\s*["']([^"']+)["']`)

type MangaBook struct {
	Path       string `json:"path"`
	Title      string `json:"title"`
	PageCount  int    `json:"page_count"`
	Size       int64  `json:"size"`
	ModifiedAt string `json:"modified_at"`
	CoverURL   string `json:"cover_url"`
	Progress   int    `json:"progress_page"`
	UpdatedAt  string `json:"updated_at,omitempty"`
}

type mangaIndex struct {
	Path       string
	Title      string
	Pages      []string
	Size       int64
	ModifiedAt time.Time
}

type mangaManifestItem struct {
	ID         string `xml:"id,attr"`
	Href       string `xml:"href,attr"`
	MediaType  string `xml:"media-type,attr"`
	Properties string `xml:"properties,attr"`
}

type epubPackage struct {
	Metadata struct {
		Title string `xml:"title"`
		Meta  []struct {
			Name    string `xml:"name,attr"`
			Content string `xml:"content,attr"`
		} `xml:"meta"`
	} `xml:"metadata"`
	Manifest struct {
		Items []mangaManifestItem `xml:"item"`
	} `xml:"manifest"`
	Spine struct {
		Items []struct {
			IDRef string `xml:"idref,attr"`
		} `xml:"itemref"`
	} `xml:"spine"`
}

func (s *Server) mangaList(w http.ResponseWriter, r *http.Request) {
	progress, err := s.store.MangaProgresses(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Не удалось прочитать историю манги")
		return
	}
	books := make([]MangaBook, 0)
	err = filepath.WalkDir(s.cfg.MediaRoot, func(filename string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if filename != s.cfg.MediaRoot && strings.HasPrefix(entry.Name(), ".") {
				return fs.SkipDir
			}
			return nil
		}
		if len(books) >= maxMangaBooks {
			return fs.SkipAll
		}
		if strings.HasPrefix(entry.Name(), ".") || !strings.EqualFold(filepath.Ext(entry.Name()), ".epub") {
			return nil
		}
		relative, relErr := filepath.Rel(s.cfg.MediaRoot, filename)
		if relErr != nil {
			return relErr
		}
		relative = filepath.ToSlash(relative)
		index, indexErr := s.getMangaIndex(relative)
		if indexErr != nil {
			if s.logger != nil {
				s.logger.Warn("skip unreadable EPUB", "file", entry.Name(), "error", indexErr)
			}
			return nil
		}
		item := MangaBook{
			Path: index.Path, Title: index.Title, PageCount: len(index.Pages), Size: index.Size,
			ModifiedAt: index.ModifiedAt.UTC().Format(time.RFC3339),
			CoverURL:   "/api/manga/page?path=" + url.QueryEscape(index.Path) + "&page=0",
		}
		if saved, ok := progress[index.Path]; ok {
			item.Progress = clampInt(saved.Page, 0, len(index.Pages)-1)
			item.UpdatedAt = saved.UpdatedAt
		}
		books = append(books, item)
		return nil
	})
	if err != nil {
		s.logger.Error("scan manga", "error", err)
		writeError(w, http.StatusInternalServerError, "Не удалось просканировать мангу")
		return
	}
	sort.Slice(books, func(i, j int) bool {
		if books[i].UpdatedAt != books[j].UpdatedAt {
			return books[i].UpdatedAt > books[j].UpdatedAt
		}
		if books[i].ModifiedAt != books[j].ModifiedAt {
			return books[i].ModifiedAt > books[j].ModifiedAt
		}
		return strings.ToLower(books[i].Title) < strings.ToLower(books[j].Title)
	})
	writeJSON(w, http.StatusOK, books)
}

func (s *Server) mangaPage(w http.ResponseWriter, r *http.Request) {
	requestedPath := r.URL.Query().Get("path")
	index, err := s.getMangaIndex(requestedPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			writeError(w, http.StatusNotFound, "EPUB не найден")
		} else {
			writeError(w, http.StatusBadRequest, "Не удалось открыть эту книгу")
		}
		return
	}
	// The page parameter is zero-based so it matches the range input in the reader.
	pageNumber, parseErr := strconv.Atoi(r.URL.Query().Get("page"))
	if parseErr != nil || pageNumber < 0 || pageNumber >= len(index.Pages) {
		writeError(w, http.StatusBadRequest, "Некорректный номер страницы")
		return
	}
	filename, err := safeMangaPath(s.cfg.MediaRoot, index.Path)
	if err != nil {
		writeError(w, http.StatusNotFound, "EPUB не найден")
		return
	}
	archive, err := zip.OpenReader(filename)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Не удалось прочитать EPUB")
		return
	}
	defer archive.Close()
	var pageFile *zip.File
	for _, candidate := range archive.File {
		if candidate.Name == index.Pages[pageNumber] {
			pageFile = candidate
			break
		}
	}
	if pageFile == nil || pageFile.UncompressedSize64 > maxEPUBPageBytes {
		writeError(w, http.StatusUnprocessableEntity, "Страница слишком большая или отсутствует")
		return
	}
	reader, err := pageFile.Open()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Не удалось открыть страницу")
		return
	}
	defer reader.Close()
	contentType := mangaImageContentType(filepath.Ext(pageFile.Name))
	if contentType == "" {
		writeError(w, http.StatusUnsupportedMediaType, "Формат страницы не поддерживается браузером")
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", fmt.Sprint(pageFile.UncompressedSize64))
	w.Header().Set("Cache-Control", "private, max-age=3600")
	_, _ = io.CopyN(w, reader, int64(pageFile.UncompressedSize64))
}

func (s *Server) mangaProgressUpdate(w http.ResponseWriter, r *http.Request) {
	var input store.MangaProgress
	if !decodeJSON(w, r, &input) {
		return
	}
	index, err := s.getMangaIndex(input.Path)
	if err != nil {
		writeError(w, http.StatusNotFound, "Книга не найдена")
		return
	}
	if input.PageCount != len(index.Pages) || input.Page < 0 || input.Page >= len(index.Pages) {
		writeError(w, http.StatusBadRequest, "Некорректный прогресс чтения")
		return
	}
	input.Path = index.Path
	if err := s.store.SetMangaProgress(r.Context(), input); err != nil {
		writeError(w, http.StatusInternalServerError, "Не удалось сохранить прогресс чтения")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) getMangaIndex(requested string) (mangaIndex, error) {
	filename, err := safeMangaPath(s.cfg.MediaRoot, requested)
	if err != nil {
		return mangaIndex{}, err
	}
	info, err := os.Stat(filename)
	if err != nil {
		return mangaIndex{}, err
	}
	key := filepath.ToSlash(filepath.Clean(filepath.FromSlash(requested)))
	s.mangaMu.Lock()
	defer s.mangaMu.Unlock()
	if cached, ok := s.mangaCache[key]; ok && cached.Size == info.Size() && cached.ModifiedAt.Equal(info.ModTime()) {
		return cached, nil
	}
	parsed, err := parseEPUB(key, filename, info)
	if err != nil {
		return mangaIndex{}, err
	}
	if s.mangaCache == nil {
		s.mangaCache = make(map[string]mangaIndex)
	}
	s.mangaCache[key] = parsed
	return parsed, nil
}

func safeMangaPath(root, requested string) (string, error) {
	if requested == "" || len(requested) > 2048 {
		return "", fs.ErrInvalid
	}
	clean := filepath.Clean(filepath.FromSlash(requested))
	if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || !strings.EqualFold(filepath.Ext(clean), ".epub") {
		return "", fs.ErrInvalid
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	resolvedPath, err := filepath.EvalSymlinks(filepath.Join(resolvedRoot, clean))
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(resolvedRoot, resolvedPath)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fs.ErrInvalid
	}
	info, err := os.Stat(resolvedPath)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fs.ErrInvalid
	}
	return resolvedPath, nil
}

func parseEPUB(relative, filename string, info fs.FileInfo) (mangaIndex, error) {
	archive, err := zip.OpenReader(filename)
	if err != nil {
		return mangaIndex{}, err
	}
	defer archive.Close()
	if len(archive.File) > maxEPUBEntries {
		return mangaIndex{}, errors.New("too many EPUB entries")
	}
	entries := make(map[string]*zip.File, len(archive.File))
	for _, entry := range archive.File {
		name := path.Clean(entry.Name)
		if name != "." && !strings.HasPrefix(name, "../") && !strings.HasPrefix(name, "/") {
			entries[name] = entry
		}
	}
	containerData, err := readEPUBEntry(entries["META-INF/container.xml"], maxEPUBPackageBytes)
	if err != nil {
		return mangaIndex{}, errors.New("missing EPUB container")
	}
	var container struct {
		Rootfiles []struct {
			FullPath string `xml:"full-path,attr"`
		} `xml:"rootfiles>rootfile"`
	}
	if err := xml.Unmarshal(containerData, &container); err != nil || len(container.Rootfiles) == 0 {
		return mangaIndex{}, errors.New("invalid EPUB container")
	}
	opfPath := cleanArchiveHref("", container.Rootfiles[0].FullPath)
	opfData, err := readEPUBEntry(entries[opfPath], maxEPUBPackageBytes)
	if err != nil {
		return mangaIndex{}, errors.New("missing EPUB package document")
	}
	var pkg epubPackage
	if err := xml.Unmarshal(opfData, &pkg); err != nil {
		return mangaIndex{}, errors.New("invalid EPUB package document")
	}
	manifest := make(map[string]mangaManifestItem, len(pkg.Manifest.Items))
	manifestPaths := make(map[string]mangaManifestItem, len(pkg.Manifest.Items))
	for _, item := range pkg.Manifest.Items {
		resolved := cleanArchiveHref(path.Dir(opfPath), item.Href)
		if resolved == "" || !isArchiveImage(item.MediaType, resolved) {
			continue
		}
		item.Href = resolved
		manifest[item.ID] = item
		manifestPaths[resolved] = item
	}
	pages := make([]string, 0, len(manifestPaths))
	seen := make(map[string]bool)
	appendPage := func(candidate string) {
		candidate = path.Clean(candidate)
		if _, exists := manifestPaths[candidate]; exists && !seen[candidate] {
			pages = append(pages, candidate)
			seen[candidate] = true
		}
	}
	for _, ref := range pkg.Spine.Items {
		item, ok := findManifestItem(pkg.Manifest.Items, ref.IDRef)
		if !ok {
			continue
		}
		resolved := cleanArchiveHref(path.Dir(opfPath), item.Href)
		if isArchiveImage(item.MediaType, resolved) {
			appendPage(resolved)
			continue
		}
		if !strings.Contains(strings.ToLower(item.MediaType), "xhtml") {
			continue
		}
		document, readErr := readEPUBEntry(entries[resolved], maxEPUBPackageBytes)
		if readErr != nil {
			continue
		}
		for _, reference := range epubImageReferences(document) {
			appendPage(cleanArchiveHref(path.Dir(resolved), reference))
		}
	}
	if len(pages) == 0 {
		for _, item := range pkg.Manifest.Items {
			resolved := cleanArchiveHref(path.Dir(opfPath), item.Href)
			if isArchiveImage(item.MediaType, resolved) {
				appendPage(resolved)
			}
		}
		sort.SliceStable(pages, func(i, j int) bool { return strings.ToLower(pages[i]) < strings.ToLower(pages[j]) })
	}
	if len(pages) == 0 {
		return mangaIndex{}, errors.New("EPUB has no readable images")
	}
	coverPath := ""
	for _, item := range pkg.Manifest.Items {
		if containsWord(item.Properties, "cover-image") {
			coverPath = cleanArchiveHref(path.Dir(opfPath), item.Href)
			break
		}
	}
	if coverPath == "" {
		for _, meta := range pkg.Metadata.Meta {
			if strings.EqualFold(meta.Name, "cover") {
				if item, ok := manifest[meta.Content]; ok {
					coverPath = item.Href
					break
				}
			}
		}
	}
	if coverPath != "" && !seen[coverPath] {
		pages = append([]string{coverPath}, pages...)
	}
	title := strings.TrimSpace(html.UnescapeString(pkg.Metadata.Title))
	if title == "" {
		title = strings.TrimSuffix(filepath.Base(relative), filepath.Ext(relative))
		title = strings.NewReplacer("_", " ", ".", " ").Replace(title)
	}
	return mangaIndex{Path: relative, Title: title, Pages: pages, Size: info.Size(), ModifiedAt: info.ModTime()}, nil
}

func findManifestItem(items []mangaManifestItem, id string) (mangaManifestItem, bool) {
	for _, item := range items {
		if item.ID == id {
			return item, true
		}
	}
	return mangaManifestItem{}, false
}

func readEPUBEntry(file *zip.File, limit int64) ([]byte, error) {
	if file == nil || file.UncompressedSize64 > uint64(limit) {
		return nil, errors.New("EPUB entry exceeds size limit")
	}
	reader, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, errors.New("invalid EPUB entry")
	}
	return data, nil
}

func cleanArchiveHref(base, reference string) string {
	parsed, err := url.Parse(strings.TrimSpace(reference))
	if err != nil || parsed.IsAbs() {
		return ""
	}
	unescaped, err := url.PathUnescape(parsed.EscapedPath())
	if err != nil {
		return ""
	}
	clean := path.Clean(path.Join(base, unescaped))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") {
		return ""
	}
	return clean
}

func epubImageReferences(document []byte) []string {
	refs := make([]string, 0)
	decoder := xml.NewDecoder(strings.NewReader(string(document)))
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}
		start, ok := token.(xml.StartElement)
		if !ok {
			continue
		}
		for _, attr := range start.Attr {
			if (start.Name.Local == "img" && attr.Name.Local == "src") || (start.Name.Local == "image" && attr.Name.Local == "href") {
				refs = append(refs, html.UnescapeString(attr.Value))
			}
		}
	}
	if len(refs) == 0 {
		for _, match := range imageAttributePattern.FindAllSubmatch(document, -1) {
			refs = append(refs, html.UnescapeString(string(match[1])))
		}
	}
	return refs
}

func isArchiveImage(mediaType, filename string) bool {
	ext := strings.ToLower(path.Ext(filename))
	if mediaType != "" && strings.HasPrefix(strings.ToLower(mediaType), "image/") {
		return mangaImageContentType(ext) != ""
	}
	return mangaImageContentType(ext) != ""
}

func mangaImageContentType(ext string) string {
	switch strings.ToLower(ext) {
	case ".avif":
		return "image/avif"
	case ".gif":
		return "image/gif"
	case ".jpeg", ".jpg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".webp":
		return "image/webp"
	default:
		return ""
	}
}

func clampInt(value, minimum, maximum int) int {
	if value < minimum {
		return minimum
	}
	if value > maximum {
		return maximum
	}
	return value
}

func containsWord(value, expected string) bool {
	for _, word := range strings.Fields(value) {
		if word == expected {
			return true
		}
	}
	return false
}
