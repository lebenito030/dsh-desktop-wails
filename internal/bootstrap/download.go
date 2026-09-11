package bootstrap

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// Download 把 url 拉到 dest（.part 中转再原子改名），按字节回调进度。
// total<=0（服务端无 Content-Length）时回调仍逐块触发，由上层显示不确定态。
func Download(ctx context.Context, url, dest string, onProgress func(got, total int64)) error {
	part := dest + ".part"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载 %s 返回 %s", url, resp.Status)
	}

	f, err := os.Create(part)
	if err != nil {
		return err
	}

	total := resp.ContentLength
	var got int64
	buf := make([]byte, 64*1024)
	var lastReport time.Time
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				f.Close()
				return werr
			}
			got += int64(n)
			// 节流到 ~20fps，避免高频回调淹没前端。
			if now := time.Now(); now.Sub(lastReport) >= 50*time.Millisecond || rerr != nil {
				lastReport = now
				if onProgress != nil {
					onProgress(got, total)
				}
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			f.Close()
			return rerr
		}
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(part, dest); err != nil {
		return err
	}
	if onProgress != nil {
		onProgress(got, total)
	}
	return nil
}
