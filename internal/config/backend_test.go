package config

import "testing"

func TestResolveBackendPreservesFileAndResolvesCredentialFreeS3(t *testing.T) {
	file, err := ResolveBackend("file:///mnt/camp", S3Values{})
	if err != nil {
		t.Fatal(err)
	}
	if file.Kind != BackendFile || file.File.Root != "/mnt/camp" {
		t.Fatalf("file backend = %#v", file)
	}

	s3, err := ResolveBackend("s3://camp-bucket/team/camp", S3Values{
		Endpoint: "https://minio.example.test:9000", Region: "us-east-1", PathStyle: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if s3.Kind != BackendS3 || s3.S3.Bucket != "camp-bucket" || s3.S3.Prefix != "team/camp" || s3.S3.Endpoint != "https://minio.example.test:9000" || !s3.S3.PathStyle {
		t.Fatalf("S3 backend = %#v", s3)
	}
	if s3.SanitizedURL != "s3://camp-bucket/team/camp" || len(s3.Fingerprint) != 64 {
		t.Fatalf("S3 identity = %#v", s3)
	}
}

func TestResolveBackendRejectsCredentialsAndUnsafeS3Configuration(t *testing.T) {
	for _, test := range []struct {
		raw    string
		values S3Values
	}{
		{raw: "s3://user:secret@bucket/prefix", values: S3Values{Endpoint: "https://s3.example", Region: "us-east-1"}},
		{raw: "s3://bucket/../escape", values: S3Values{Endpoint: "https://s3.example", Region: "us-east-1"}},
		{raw: "s3://bucket/prefix", values: S3Values{Endpoint: "https://token@s3.example", Region: "us-east-1"}},
		{raw: "s3://bucket/prefix", values: S3Values{Endpoint: "http://s3.example", Region: ""}},
		{raw: "s3://bucket/prefix", values: S3Values{Endpoint: "http://s3.example", Region: "us-east-1"}},
		{raw: "s3://bucket/prefix", values: S3Values{Endpoint: "https://s3.example", Region: "us-east-1", Insecure: true}},
		{raw: "s3://Bad_Bucket/prefix", values: S3Values{Endpoint: "https://s3.example", Region: "us-east-1"}},
		{raw: "s3://192.168.1.1/prefix", values: S3Values{Endpoint: "https://s3.example", Region: "us-east-1"}},
		{raw: "s3://dotted.bucket/prefix", values: S3Values{Endpoint: "https://s3.example", Region: "us-east-1"}},
		{raw: "s3://camp-bucket/prefix", values: S3Values{Endpoint: "http://127.0.0.1:9000", Region: "us-east-1", Insecure: true}},
		{raw: "s3://camp-bucket/prefix", values: S3Values{Endpoint: "https://[::1]:9000", Region: "us-east-1"}},
	} {
		if _, err := ResolveBackend(test.raw, test.values); err == nil {
			t.Fatalf("ResolveBackend(%q, %#v) succeeded", test.raw, test.values)
		}
	}
}

func TestResolveBackendAllowsDottedBucketWithPathStyleHTTPS(t *testing.T) {
	backend, err := ResolveBackend("s3://dotted.bucket/prefix", S3Values{
		Endpoint: "https://s3.example", Region: "us-east-1", PathStyle: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !backend.S3.PathStyle || backend.S3.Bucket != "dotted.bucket" {
		t.Fatalf("resolved backend = %#v", backend)
	}
}

func TestResolveBackendAllowsIPLiteralEndpointWithPathStyle(t *testing.T) {
	backend, err := ResolveBackend("s3://camp-bucket/prefix", S3Values{
		Endpoint: "http://127.0.0.1:9000", Region: "us-east-1", PathStyle: true, Insecure: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !backend.S3.PathStyle {
		t.Fatalf("resolved backend = %#v", backend)
	}
}

func TestResolveBackendAllowsExplicitInsecureHTTP(t *testing.T) {
	backend, err := ResolveBackend("s3://camp-bucket/prefix", S3Values{
		Endpoint: "http://minio.internal:9000", Region: "us-east-1", Insecure: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !backend.S3.Insecure {
		t.Fatal("resolved backend lost explicit insecure policy")
	}
}
