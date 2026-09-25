package cache

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Ullauri/shapediff/internal/gitlab"
)

func TestKeyStableAndDistinct(t *testing.T) {
	k := Key{Repo: "group/svc", SHA: "0123456789abcdef0123", ExtractorVersion: "0.1.0", Toolchain: "go1.26.0", BuilderMode: "InstantiateGenerics", ConfigHash: "c1"}
	s1 := k.String()
	if s1 != k.String() {
		t.Fatal("unstable")
	}
	if strings.ContainsAny(s1, "/ \\:") {
		t.Fatalf("not filesystem safe: %s", s1)
	}
	for name, mut := range map[string]func(*Key){
		"toolchain": func(k *Key) { k.Toolchain = "go1.25.0" },
		"builder":   func(k *Key) { k.BuilderMode = "" },
		"config":    func(k *Key) { k.ConfigHash = "c2" },
		"version":   func(k *Key) { k.ExtractorVersion = "0.2.0" },
		"sha":       func(k *Key) { k.SHA = "ffff" },
	} {
		k2 := k
		mut(&k2)
		if k2.String() == s1 {
			t.Errorf("%s change did not change key", name)
		}
	}
	if MRKey("group/svc", 12, "0123456789abcdef") != "delta_group-svc_mr12_0123456789ab" {
		t.Errorf("MRKey = %s", MRKey("group/svc", 12, "0123456789abcdef"))
	}
}

func TestDirStore(t *testing.T) {
	d := Dir{Path: t.TempDir() + "/sub"}
	ctx := context.Background()
	if _, ok, err := d.Get(ctx, "k"); ok || err != nil {
		t.Fatalf("absent: %v %v", ok, err)
	}
	if err := d.Put(ctx, "k", []byte("v")); err != nil {
		t.Fatal(err)
	}
	b, ok, err := d.Get(ctx, "k")
	if err != nil || !ok || string(b) != "v" {
		t.Fatalf("get = %q %v %v", b, ok, err)
	}
}

func TestPackagesStoreAndFromEnv(t *testing.T) {
	stored := map[string][]byte{}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PUT" {
			b, _ := io.ReadAll(r.Body)
			stored[r.URL.Path] = b
			w.WriteHeader(201)
			json.NewEncoder(w).Encode(map[string]string{"message": "201 Created"})
			return
		}
		b, ok := stored[r.URL.Path]
		if !ok {
			w.WriteHeader(404)
			return
		}
		w.Write(b)
	}))
	defer s.Close()
	c := &gitlab.Client{BaseURL: s.URL + "/api/v4", Token: "t", HTTP: s.Client()}
	st, err := FromEnv(EnvSpec{Value: "gitlab", Client: c, Project: gitlab.ProjectID(3)})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, ok, _ := st.Get(ctx, "graph_x"); ok {
		t.Fatal("should be absent")
	}
	if err := st.Put(ctx, "graph_x", []byte("{}")); err != nil {
		t.Fatal(err)
	}
	if b, ok, err := st.Get(ctx, "graph_x"); !ok || err != nil || string(b) != "{}" {
		t.Fatalf("get = %q %v %v", b, ok, err)
	}
	if _, err := FromEnv(EnvSpec{Value: "gitlab"}); err == nil {
		t.Error("gitlab without client should fail")
	}
	if d, err := FromEnv(EnvSpec{Value: "dir:/tmp/x"}); err != nil || d.(Dir).Path != "/tmp/x" {
		t.Errorf("dir: %v %v", d, err)
	}
	if d, err := FromEnv(EnvSpec{ProjectDir: "/p"}); err != nil || d.(Dir).Path != "/p/.shapediff-cache" {
		t.Errorf("default: %v %v", d, err)
	}
	if _, err := FromEnv(EnvSpec{Value: "s3://x"}); err == nil {
		t.Error("unknown backend accepted")
	}
}
