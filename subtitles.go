package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"path"
	"strings"
)

var subtitleExtensions = map[string]bool{".srt": true, ".ass": true, ".ssa": true, ".vtt": true}

func (e *Engine) copyMediaSubtitles(ctx context.Context, a Account, files []Entry, media Entry, relative string, r Rule, j *Job) {
	if !r.CopySubtitles || j.DryRun {
		return
	}
	stem := strings.TrimSuffix(media.Name, path.Ext(media.Name))
	for _, f := range files {
		if r.Filter.excluded(f.Name, path.Join(relative, f.Name)) {
			continue
		}
		extension := strings.ToLower(path.Ext(f.Name))
		subStem := strings.TrimSuffix(f.Name, path.Ext(f.Name))
		if f.Dir || !subtitleExtensions[extension] || subStem != stem && !strings.HasPrefix(subStem, stem+".") {
			continue
		}
		name, err := safeName(f.Name)
		if err != nil {
			j.Errors++
			continue
		}
		outcome := "skipped"
		target, err := e.clouds.Resolve(ctx, Media{Account: a.ID, RemoteID: f.ID, PickCode: f.PickCode, Name: f.Name})
		if err == nil {
			var content []byte
			if e.subtitleDownload != nil {
				content, err = e.subtitleDownload(ctx, target)
			} else {
				content, err = readSubtitle(ctx, target)
			}
			if err == nil {
				var created bool
				created, err = e.publish(path.Join(r.Output, relative, name), "subtitle:"+e.s.mediaID(a, f.ID), content)
				if created {
					j.Sidecars++
					outcome = "created"
				} else if err == nil {
					j.SidecarsSkipped++
				}
			}
		}
		if err != nil {
			j.Errors++
			j.Message = "字幕复制失败：" + f.Name
			if len(j.Failures) < 20 {
				j.Failures = append(j.Failures, j.Message+"（"+err.Error()+"）")
			}
		}
		if err != nil {
			outcome = "failed"
		}
		e.generationActivity(a, r, *j, f, path.Join(r.SourcePath, relative, f.Name), generationOutputName(e, path.Join(r.Output, relative, name)), outcome, "subtitle", err)
	}
}
func readSubtitle(ctx context.Context, target PlaybackTarget) ([]byte, error) {
	return readCompanion(ctx, target, 8<<20, false)
}

// Common library sidecars only; STRM, playlists and media are never copied here.
var nonMediaExtensions = map[string]bool{
	".nfo": true, ".jpg": true, ".jpeg": true, ".png": true, ".webp": true,
	".gif": true, ".bmp": true, ".tbn": true, ".json": true, ".xml": true, ".txt": true,
}

func (e *Engine) copyNonMedia(ctx context.Context, a Account, f Entry, relative string, r Rule, j *Job) {
	if !r.CopyNonMedia || j.DryRun || f.Dir || !nonMediaExtensions[strings.ToLower(path.Ext(f.Name))] {
		return
	}
	name, err := safeName(f.Name)
	outcome := "skipped"
	if err == nil && f.Size > 32<<20 {
		err = errors.New("单个非媒体文件超过 32MB")
	}
	if err == nil {
		var target PlaybackTarget
		target, err = e.clouds.Resolve(ctx, Media{Account: a.ID, RemoteID: f.ID, PickCode: f.PickCode, Name: f.Name})
		if err == nil {
			var content []byte
			if e.companionDownload != nil {
				content, err = e.companionDownload(ctx, target)
			} else {
				content, err = readCompanion(ctx, target, 32<<20, strings.EqualFold(path.Ext(f.Name), ".json"))
			}
			if err == nil && int64(len(content)) > 32<<20 {
				err = errors.New("单个非媒体文件超过 32MB")
			}
			if err == nil {
				var created bool
				created, err = e.publish(path.Join(r.Output, relative, name), "nonmedia:"+e.s.mediaID(a, f.ID), content)
				if created {
					j.NonMediaFiles++
					outcome = "created"
				} else if err == nil {
					j.NonMediaSkipped++
				}
			}
		}
	}
	if err != nil {
		j.Errors++
		j.Message = "非媒体文件复制失败：" + f.Name
		if len(j.Failures) < 20 {
			j.Failures = append(j.Failures, j.Message+"（"+err.Error()+"）")
		}
	}
	if err != nil {
		outcome = "failed"
	}
	e.generationActivity(a, r, *j, f, path.Join(r.SourcePath, relative, f.Name), generationOutputName(e, path.Join(r.Output, relative, name)), outcome, "nonmedia", err)
}

func readCompanion(ctx context.Context, target PlaybackTarget, maxBytes int64, allowJSON bool) ([]byte, error) {
	client := safeMediaClient()
	defer client.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, "GET", target.URL, nil)
	if err != nil {
		return nil, errors.New("文件下载地址无效")
	}
	req.Header = target.Headers.Clone()
	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.New("文件下载失败")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, errors.New("文件下载接口拒绝请求")
	}
	kind := strings.ToLower(resp.Header.Get("Content-Type"))
	if strings.Contains(kind, "text/html") || !allowJSON && strings.Contains(kind, "application/json") {
		return nil, errors.New("下载地址返回了网页或接口数据")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, errors.New("文件读取失败")
	}
	if len(data) == 0 || int64(len(data)) > maxBytes {
		return nil, errors.New("文件为空或超过大小上限")
	}
	return data, nil
}
