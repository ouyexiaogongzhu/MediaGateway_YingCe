package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"strconv"
	"time"

	"infinite-canvas/backend/internal/service"

	"github.com/gin-gonic/gin"
)

// 视频超分测试工具：浏览器上传 → MediaGateway /v1/upscale 的薄代理。
// 不入资源库、不建任务记录；状态轮询与下载也走代理，前端自 10s 轮询。

const toolsUpscaleMaxBytes = int64(2) << 30 // 2GB 视频源文件上限

func toolsUpscaleUpload(svc *service.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		policy, available := loadRuntimePolicy(c, svc)
		if !available || !enforceRateLimit(c, "tools-upscale:"+user.ID, policy.Request.TaskCreatePerMinute, time.Minute) {
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, toolsUpscaleMaxBytes)
		resolution := c.PostForm("resolution")
		if resolution != "1080" && resolution != "2160" {
			fail(c, http.StatusBadRequest, errors.New("resolution 必须是 1080 或 2160"))
			return
		}

		// 两个分支只构造 request，响应处理共用。
		var request *http.Request
		log.Printf("[tools-upscale] resource_id=%q video_field=%v", c.PostForm("resource_id"), c.Request.MultipartForm != nil)
		if resourceID := c.PostForm("resource_id"); resourceID != "" {
			// 素材库资源：gateway 同机落库，直接传绝对路径，免 2GB 重传。
			path, err := svc.ResourceLocalFilePath(user.ID, resourceID)
			if err != nil {
				failService(c, err)
				return
			}
			body, err := json.Marshal(map[string]string{"video_path": path, "resolution": resolution})
			if err != nil {
				fail(c, http.StatusInternalServerError, err)
				return
			}
			request, err = http.NewRequestWithContext(c.Request.Context(), http.MethodPost, service.MediaGatewayBaseURL()+"/v1/upscale", bytes.NewReader(body))
			if err != nil {
				fail(c, http.StatusInternalServerError, err)
				return
			}
			request.Header.Set("Content-Type", "application/json")
		} else {
			fileHeader, err := c.FormFile("video")
			if err != nil {
				fail(c, http.StatusBadRequest, err)
				return
			}
			src, err := fileHeader.Open()
			if err != nil {
				fail(c, http.StatusBadRequest, err)
				return
			}
			defer src.Close()

			// 流式重拼 multipart：文件可能到 2GB，不落内存。
			pr, pw := io.Pipe()
			writer := multipart.NewWriter(pw)
			go func() {
				var goErr error
				var part io.Writer
				if part, goErr = writer.CreateFormFile("video", fileHeader.Filename); goErr == nil {
					_, goErr = io.Copy(part, src)
				}
				if goErr == nil {
					goErr = writer.WriteField("resolution", resolution)
				}
				if goErr == nil {
					goErr = writer.Close()
				}
				_ = pw.CloseWithError(goErr)
			}()
			request, err = http.NewRequestWithContext(c.Request.Context(), http.MethodPost, service.MediaGatewayBaseURL()+"/v1/upscale", pr)
			if err != nil {
				fail(c, http.StatusInternalServerError, err)
				return
			}
			request.Header.Set("Content-Type", writer.FormDataContentType())
		}
		client := service.OutboundHTTPClient(30 * time.Minute)
		response, err := client.Do(request)
		if err != nil {
			fail(c, http.StatusBadGateway, err)
			return
		}
		defer response.Body.Close()
		payload, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		if err != nil {
			fail(c, http.StatusBadGateway, err)
			return
		}
		if response.StatusCode != http.StatusOK {
			fail(c, http.StatusBadGateway, errors.New("gateway /v1/upscale: "+string(payload)))
			return
		}
		var created struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(payload, &created); err != nil || created.ID == "" {
			fail(c, http.StatusBadGateway, errors.New("gateway /v1/upscale 未返回任务 id"))
			return
		}
		ok(c, gin.H{"id": created.ID})
	}
}

func toolsUpscaleStatus(svc *service.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		if _, err := currentUser(c, svc); err != nil {
			failService(c, err)
			return
		}
		request, err := http.NewRequestWithContext(c.Request.Context(), http.MethodGet, service.MediaGatewayBaseURL()+"/v1/videos/"+c.Param("id"), nil)
		if err != nil {
			fail(c, http.StatusInternalServerError, err)
			return
		}
		response, err := service.OutboundHTTPClient(30 * time.Second).Do(request)
		if err != nil {
			fail(c, http.StatusBadGateway, err)
			return
		}
		defer response.Body.Close()
		payload, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		if err != nil {
			fail(c, http.StatusBadGateway, err)
			return
		}
		if response.StatusCode != http.StatusOK {
			fail(c, response.StatusCode, errors.New("gateway /v1/videos: "+string(payload)))
			return
		}
		var status struct {
			Status   string  `json:"status"`
			Progress float64 `json:"progress"`
			Error    string  `json:"error"`
		}
		if err := json.Unmarshal(payload, &status); err != nil {
			fail(c, http.StatusBadGateway, err)
			return
		}
		ok(c, status)
	}
}

func toolsUpscaleContent(svc *service.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		if _, err := currentUser(c, svc); err != nil {
			failService(c, err)
			return
		}
		id := c.Param("id")
		request, err := http.NewRequestWithContext(c.Request.Context(), http.MethodGet, service.MediaGatewayBaseURL()+"/v1/videos/"+id+"/content", nil)
		if err != nil {
			fail(c, http.StatusInternalServerError, err)
			return
		}
		response, err := service.OutboundHTTPClient(30 * time.Minute).Do(request)
		if err != nil {
			fail(c, http.StatusBadGateway, err)
			return
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			payload, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
			fail(c, response.StatusCode, errors.New("gateway /v1/videos content: "+string(payload)))
			return
		}
		if disposition := response.Header.Get("Content-Disposition"); disposition != "" {
			c.Header("Content-Disposition", disposition)
		} else {
			c.Header("Content-Disposition", "attachment; filename=\"upscale-"+id+".mp4\"")
		}
		if contentType := response.Header.Get("Content-Type"); contentType != "" {
			c.Header("Content-Type", contentType)
		}
		if response.ContentLength > 0 {
			c.Header("Content-Length", strconv.FormatInt(response.ContentLength, 10))
		}
		_, _ = io.Copy(c.Writer, response.Body)
	}
}
