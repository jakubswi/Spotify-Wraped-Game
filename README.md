# Spotify Wrapped - DevOps Infrastructure

This document outlines the DevOps, containerization, and cloud infrastructure setup for the **Spotify Wrapped** application.

## Overview
The application is a Python Flask web service served by Gunicorn, sitting behind an Nginx reverse proxy. It is designed to be run as a containerized workload, with support for local development via Docker Compose and production deployment on Google Kubernetes Engine (GKE) managed by Terraform.

## Containerization (Docker)
The application is packaged using a production-ready `Dockerfile`:
- **Base Image:** `python:3.10-slim` for a lightweight footprint.
- **Security:** Runs as a non-root user (`appgroup`) to minimize privilege escalation risks.
- **Optimizations:** Sets `PYTHONDONTWRITEBYTECODE` and `PYTHONUNBUFFERED`, and leverages Docker layer caching by copying `requirements.txt` before the application code.
- **Web Server:** Uses `gunicorn` with 3 workers bound to port 5000 to serve the Flask application efficiently.

## Local Development (Docker Compose)
A `docker-compose.yml` is provided for seamless local development and testing.
- **Services:**
  - `app`: Builds the Flask application from the local `Dockerfile`.
  - `nginx`: Uses `nginx:alpine` and mounts the local `./nginx/nginx.conf` to act as a reverse proxy.
- **Secrets Management:** Utilizes Docker Compose secrets (`./secrets/CLIENT_ID.txt` and `./secrets/CLIENT_SECRET.txt`) which are mounted securely into the container at `/run/secrets/`.
- **Networking:** Nginx listens on port 80 and forwards traffic to the `app` container on port 5000.

## Cloud Infrastructure & Kubernetes
The production infrastructure is hosted on Google Cloud Platform (GCP) and orchestrated using **Terraform**.

### Terraform (`main.tf`)
- **GKE Cluster:** Provisions a Google Kubernetes Engine (GKE) cluster named `spotify-wraped-service-test` running in **Autopilot** mode in the `europe-central2` region.
- **Kubernetes Resources Managed by Terraform:**
  - **Secrets:** Creates a Kubernetes `Secret` (`spotify-secrets`) to store Spotify API credentials (`CLIENT_ID` and `CLIENT_SECRET`).
  - **ConfigMap:** Provisions a `ConfigMap` (`nginx-config`) containing the Nginx reverse proxy configuration.
  - **Deployment:** Deploys a `spotify-wrapped-deployment` which uses a **multi-container Pod pattern** (sidecar):
    - **App Container:** Pulls the image from GCP Artifact Registry. It includes a `/health` liveness probe, mounts the secrets volume, and runs with a restricted security context.
    - **Nginx Sidecar:** Runs `nginx:alpine`, sharing the same network namespace as the app (routing to `127.0.0.1:5000`). It mounts the Nginx ConfigMap.
  - **Service:** Exposes the deployment to the internet via a `LoadBalancer` service (`spotify-wrapped-service`) on port 80.

### Raw Kubernetes Manifests (`k8s-deployment.yaml`)
In addition to Terraform, standard Kubernetes manifests are available for deploying the application. These manifests define the same Secret, ConfigMap, and Deployment structures, which is useful for CI/CD pipelines or manual application via `kubectl`.

## Security Posture
- **Non-root Containers:** The `Dockerfile` specifically creates and utilizes a non-root user for the application.
- **Kubernetes Security Contexts:** The deployment manifests and Terraform code enforce security contexts, such as `allow_privilege_escalation = false` and default seccomp profiles (`RuntimeDefault`).
- **Secret Management:** Credentials are never hardcoded in the codebase. They are injected via Docker Secrets locally or Kubernetes Secrets in production and read securely at runtime from `/run/secrets/`.