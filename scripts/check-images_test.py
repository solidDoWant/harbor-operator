#!/usr/bin/env python3
"""Tests scripts/check-images.py: one good and one bad reference per file kind."""

import importlib.util
import pathlib
import unittest

SCRIPT = pathlib.Path(__file__).resolve().parent / "check-images.py"
spec = importlib.util.spec_from_file_location("check_images", SCRIPT)
check_images = importlib.util.module_from_spec(spec)
spec.loader.exec_module(check_images)

GOOD_DIGEST = "@sha256:" + "a" * 64

CASES = [
    # (path, text, number of problems)
    ("a.go", f'const PostgresImage = "mirror.gcr.io/library/postgres:17{GOOD_DIGEST}"', 0),
    ("a.go", 'const PostgresImage = "postgres:17"', 1),
    ("a.go", 'Image:   "docker.io/library/busybox",', 1),
    ("a.go", 'Image:   "busybox",', 1),
    ("a.go", f'Image:   "ghcr.io/x/y:1{GOOD_DIGEST}",', 0),
    ("a.go", 'Image:   "ghcr.io/x/y:1",', 1),
    ("a.go", f'Image:   "gcr.io/distroless/static:nonroot{GOOD_DIGEST}",', 1),
    ("a.go", 'Image:   "localhost:5001/x:1",', 0),
    ("a.yaml", f"image: quay.io/x/y:1{GOOD_DIGEST}", 0),
    ("a.yaml", "image: quay.io/x/y:1", 1),
    ("a.yaml", f"image: registry.k8s.io/pause:3{GOOD_DIGEST}", 1),
    ("a.yaml", f"          image: ghcr.io/distribution/distribution:3{GOOD_DIGEST}", 0),
    ("a.yaml", "          image: postgres", 1),
    ("a.yaml", "      - image: docker.io/library/registry:3", 1),
    ("a.yaml", "image: {{ .Values.image }}", 0),
    ("Dockerfile", f"FROM quay.io/x/y:1{GOOD_DIGEST}", 0),
    ("Dockerfile", "FROM golang:1.25", 1),
    ("Dockerfile", "FROM scratch", 0),
    ("Dockerfile", f"FROM mirror.gcr.io/library/golang:1{GOOD_DIGEST} AS build\nFROM build", 0),
    ("run.sh", "docker run --rm -e A=b busybox true", 1),
    ("run.sh", f"docker run --rm mirror.gcr.io/library/busybox:1{GOOD_DIGEST} true", 0),
    ("run.sh", "kind create cluster --image kindest/node:v1", 1),
    ("run.sh", f"kind create cluster --image mirror.gcr.io/kindest/node:v1{GOOD_DIGEST}", 0),
    ("run.sh", 'docker build --tag "$image" .', 0),
    ("query.sql", "SELECT 1 FROM sessions", 0),
    ("notes.md", "image: postgres", 0),
]


class CheckImagesTest(unittest.TestCase):
    def test_cases(self):
        for path, text, want in CASES:
            with self.subTest(path=path, text=text):
                self.assertEqual(len(check_images.check_text(path, text)), want)


if __name__ == "__main__":
    unittest.main()
