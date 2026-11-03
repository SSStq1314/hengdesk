package objstore_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/SSStq1314/hengdesk/server/internal/objstore"
)

// 设置 HENGDESK_S3_IT=1 时对真实 S3（SeaweedFS / MinIO / 外部 S3）运行。
func TestS3Integration(t *testing.T) {
	if os.Getenv("HENGDESK_S3_IT") != "1" {
		t.Skip("未设置 HENGDESK_S3_IT=1，跳过 S3 集成测试")
	}
	ps, _ := strconv.ParseBool(os.Getenv("HENGDESK_S3_PATH_STYLE"))
	st, err := objstore.New(objstore.Config{
		Endpoint: os.Getenv("HENGDESK_S3_ENDPOINT"), Region: os.Getenv("HENGDESK_S3_REGION"),
		AccessKey: os.Getenv("HENGDESK_S3_ACCESS_KEY"), SecretKey: os.Getenv("HENGDESK_S3_SECRET_KEY"),
		Bucket: os.Getenv("HENGDESK_S3_BUCKET"), PathStyle: ps,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := st.EnsureBucket(ctx); err != nil {
		t.Fatal(err)
	}
	prefix := fmt.Sprintf("it-%d/", time.Now().UnixNano())
	body := []byte("衡台 HengDesk S3 集成测试")
	key := prefix + "a.txt"

	// 上传
	if err := st.Put(ctx, key, bytes.NewReader(body), int64(len(body)), "text/plain"); err != nil {
		t.Fatalf("put: %v", err)
	}
	// 下载
	rc, err := st.Get(ctx, key)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	got, _ := io.ReadAll(rc)
	_ = rc.Close()
	if !bytes.Equal(got, body) {
		t.Fatalf("get 内容不一致: %q", got)
	}
	// 列表
	keys, err := st.List(ctx, prefix)
	if err != nil || !slices.Contains(keys, key) {
		t.Fatalf("list: %v %v", keys, err)
	}
	// 预签名下载
	u, err := st.PresignGet(ctx, key, 5*time.Minute)
	if err != nil {
		t.Fatalf("presign get: %v", err)
	}
	resp, err := http.Get(u) //nolint:gosec,noctx // 测试中访问预签名地址
	if err != nil {
		t.Fatalf("presigned get: %v", err)
	}
	pb, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 200 || !bytes.Equal(pb, body) {
		t.Fatalf("presigned get status=%d body=%q", resp.StatusCode, pb)
	}
	// 预签名上传
	key2 := prefix + "b.txt"
	pu, err := st.PresignPut(ctx, key2, 5*time.Minute)
	if err != nil {
		t.Fatalf("presign put: %v", err)
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPut, pu, bytes.NewReader(body))
	resp, err = http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("presigned put: %v %v", err, resp)
	}
	_ = resp.Body.Close()
	// 删除
	for _, k := range []string{key, key2} {
		if err := st.Delete(ctx, k); err != nil {
			t.Fatalf("delete: %v", err)
		}
	}
	keys, err = st.List(ctx, prefix)
	if err != nil || len(keys) != 0 {
		t.Fatalf("删除后 list 应为空: %v %v", keys, err)
	}
}
