package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/gif"
	"image/jpeg"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/nfnt/resize"
)

var (
	transformCache = make(map[string]CachedImage)
)

func deleteAvatars(username string) error {
	avatarDir := filepath.Join(documentPath, "rotur", "avatars")
	base := strings.ToLower(username)

	extensions := []string{".gif", ".jpg"}
	for _, ext := range extensions {
		filePath := filepath.Join(avatarDir, base+ext)
		_ = os.Remove(filePath)
	}
	return nil
}

func getAvatarMetadata(username string) (string, string, string, error) {
	avatarDir := filepath.Join(documentPath, "rotur", "avatars")
	base := strings.ToLower(username)

	extensions := []string{".gif", ".jpg"}
	for _, ext := range extensions {
		filePath := filepath.Join(avatarDir, base+ext)
		info, err := os.Stat(filePath)
		if err == nil {
			contentType := "image/jpeg"
			if ext == ".gif" {
				contentType = "image/gif"
			}
			etag := fmt.Sprintf("%s-%d", username, info.ModTime().Unix())
			return filePath, contentType, etag, nil
		}
	}

	return "", "", "", os.ErrNotExist
}

func avatarHandler(c *gin.Context) {
	username, _ := strings.CutSuffix(strings.ToLower(c.Param("username")), ".gif")
	radius := c.Query("radius")
	sizeStr := c.Query("s")

	clientEtag := c.GetHeader("If-None-Match")

	filePath, contentType, baseEtag, metaErr := getAvatarMetadata(username)

	finalEtagBase := baseEtag
	if metaErr != nil {
		contentType = "image/jpeg"
		finalEtagBase = defaultImageEtag
	}

	modifierParts := []string{}
	if sizeStr != "" {
		modifierParts = append(modifierParts, "size="+sizeStr)
	}
	if radius != "" {
		modifierParts = append(modifierParts, "radius="+radius)
	}
	modifier := strings.Join(modifierParts, "-")

	if modifier == "" {
		if metaErr == nil {
			if clientEtag == fmt.Sprintf(`"%s"`, finalEtagBase) {
				c.Status(http.StatusNotModified)
				return
			}

			c.Header("ETag", fmt.Sprintf(`"%s"`, finalEtagBase))
			c.Header("Cache-Control", "public, max-age=86400, must-revalidate")
			c.File(filePath)
			return
		}
	}

	cacheKey := finalEtagBase
	if modifier != "" {
		cacheKey = cacheKey + "-" + modifier
	}

	cacheMutex.RLock()
	cached, ok := transformCache[cacheKey]
	cacheMutex.RUnlock()

	if ok {
		if clientEtag == fmt.Sprintf(`"%s"`, cacheKey) {
			c.Status(http.StatusNotModified)
			return
		}

		c.Header("ETag", fmt.Sprintf(`"%s"`, cacheKey))
		c.Header("Cache-Control", "public, max-age=86400, must-revalidate")
		c.Data(http.StatusOK, cached.ContentType, cached.Data)
		return
	}

	var imageData []byte
	if metaErr != nil {
		imageData = defaultImageContent
		contentType = "image/jpeg"
		if finalEtagBase == "" {
			finalEtagBase = defaultImageEtag
		}
	} else {
		var err error
		imageData, err = os.ReadFile(filePath)
		if err != nil {
			imageData = defaultImageContent
			contentType = "image/jpeg"
			finalEtagBase = defaultImageEtag
		}
	}

	finalEtag := cacheKey

	if contentType == "image/gif" {
		if sizeStr != "" {
			sz, err := strconv.Atoi(sizeStr)
			if err == nil && sz > 0 && sz <= 256 {
				resizedData, err := resizeGIF(imageData, sz, sz)
				if err == nil {
					imageData = resizedData
				}
			}
		}

		if radius != "" {
			radiusInt, err := strconv.Atoi(strings.TrimSuffix(radius, "px"))
			if err == nil && radiusInt > 0 {
				src, err := gif.DecodeAll(bytes.NewReader(imageData))
				if err == nil {
					rounded, err := roundGIF(src, radiusInt)
					if err == nil {
						buf := bytes.NewBuffer(nil)
						err = gif.EncodeAll(buf, rounded)
						if err == nil {
							imageData = buf.Bytes()
						}
					}
				}
			}
		}

		cacheMutex.Lock()
		transformCache[cacheKey] = CachedImage{ContentType: "image/gif", Data: imageData}
		cacheMutex.Unlock()

		if clientEtag == fmt.Sprintf(`"%s"`, finalEtag) {
			c.Status(http.StatusNotModified)
			return
		}

		c.Header("Content-Type", "image/gif")
		c.Header("Cache-Control", "public, max-age=86400, must-revalidate")
		c.Header("ETag", fmt.Sprintf(`"%s"`, finalEtag))
		c.Data(http.StatusOK, "image/gif", imageData)
		return
	}

	img, _, err := image.Decode(bytes.NewReader(imageData))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Error decoding image"})
		return
	}

	if sizeStr != "" {
		sz, err := strconv.Atoi(sizeStr)
		if err == nil && sz > 0 && sz <= 256 {
			resized := resize.Resize(uint(sz), 0, img, resize.Lanczos3)
			var buf bytes.Buffer
			jpeg.Encode(&buf, resized, &jpeg.Options{Quality: 85})
			imageData = buf.Bytes()
			finalEtag = cacheKey
		}
	}

	if radius != "" {
		radiusInt, err := strconv.Atoi(strings.TrimSuffix(radius, "px"))
		if err == nil && radiusInt > 0 {
			rounded, newContentType, err := roundCorners(imageData, radiusInt)
			if err == nil {
				imageData = rounded
				contentType = newContentType
				finalEtag = cacheKey
			}
		}
	}

	cacheMutex.Lock()
	transformCache[cacheKey] = CachedImage{ContentType: contentType, Data: imageData}
	cacheMutex.Unlock()

	if clientEtag == fmt.Sprintf(`"%s"`, finalEtag) {
		c.Status(http.StatusNotModified)
		return
	}

	maxAge := 86400
	c.Header("Content-Type", contentType)
	c.Header("Cache-Control", fmt.Sprintf("public, max-age=%d, must-revalidate", maxAge))
	c.Header("ETag", fmt.Sprintf(`"%s"`, finalEtag))
	c.Data(http.StatusOK, contentType, imageData)
}

func uploadPfpHandler(c *gin.Context) {
	var req UploadRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid JSON data"})
		return
	}

	usersFile, err := os.ReadFile("users.json")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Error reading users file"})
		return
	}

	var users []User
	if err := json.Unmarshal(usersFile, &users); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Error parsing users file: " + err.Error()})
		return
	}

	var user *User
	for i := range users {
		if users[i].Key == req.Token {
			user = &users[i]
			break
		}
	}
	if user == nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "Invalid token"})
		return
	}

	if req.Image == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Missing image"})
		return
	}

	parts := strings.Split(req.Image, ",")
	if len(parts) != 2 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid image format"})
		return
	}

	mimeHeader := parts[0]
	imageData, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid image data"})
		return
	}

	avatarDir := filepath.Join(documentPath, "rotur", "avatars")
	os.MkdirAll(avatarDir, 0755)
	username := strings.ToLower(user.Username)

	tier := strings.ToLower(toString(user.GetSubscription()))
	isPro := slices.Contains([]string{"drive", "pro", "max"}, tier)

	var ext, contentType string
	switch {
	case strings.Contains(mimeHeader, "image/gif"):
		if isPro {
			ext = ".gif"
			contentType = "image/gif"
		} else {
			// downgrade to jpg if not pro
			ext = ".jpg"
			contentType = "image/jpeg"
		}
	default:
		ext = ".jpg"
		contentType = "image/jpeg"
	}

	filePath := filepath.Join(avatarDir, username+ext)
	deleteAvatars(username)

	if contentType == "image/gif" {
		// Pro users only
		resizedData, err := resizeGIF(imageData, 256, 256)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Error resizing GIF"})
			return
		}

		err = os.WriteFile(filePath, resizedData, 0644)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Error saving GIF"})
			return
		}
	} else {
		img, _, err := image.Decode(bytes.NewReader(imageData))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Error decoding image"})
			return
		}

		resized := resize.Resize(256, 256, img, resize.Lanczos3)
		out, err := os.Create(filePath)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Error saving image"})
			return
		}
		defer out.Close()
		jpeg.Encode(out, resized, &jpeg.Options{Quality: 85})
	}

	cacheMutex.Lock()
	transformCache = make(map[string]CachedImage)
	cacheMutex.Unlock()

	c.JSON(http.StatusOK, gin.H{
		"status":  "Success",
		"message": "Profile picture uploaded successfully",
	})
}
