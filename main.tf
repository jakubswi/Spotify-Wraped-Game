provider "google" {
  project = "elite-chiller-424111-b6"
  region  = "europe-central2"
}

resource "google_container_cluster" "default" {
  name     = "spotify-wraped-service-test"
  location = "europe-central2"

  enable_autopilot = true

  deletion_protection = false
}

data "google_client_config" "default" {}

provider "kubernetes" {
  host                   = "https://${google_container_cluster.default.endpoint}"
  token                  = data.google_client_config.default.access_token
  cluster_ca_certificate = base64decode(google_container_cluster.default.master_auth[0].cluster_ca_certificate)

  ignore_annotations = [
    "^autopilot\\.gke\\.io\\/.*",
    "^cloud\\.google\\.com\\/.*"
  ]
}

resource "kubernetes_secret_v1" "spotify_secrets" {
  metadata {
    name = "spotify-secrets"
    labels = {
      app = "spotify-wrapped"
    }
  }

  data = {
    "CLIENT_ID"     = "xyz"
    "CLIENT_SECRET" = "xyz"
  }

  type = "Opaque"
}

resource "kubernetes_config_map_v1" "nginx_config" {
  metadata {
    name = "nginx-config"
    labels = {
      app = "spotify-wrapped"
    }
  }

  data = {
    "nginx.conf" = <<-EOF
      events {
          worker_connections 1024;
      }
      http {
          server {
              listen 80;
              location / {
                  proxy_pass http://127.0.0.1:5000;
                  proxy_set_header Host $host;
                  proxy_set_header X-Real-IP $remote_addr;
                  proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
                  proxy_set_header X-Forwarded-Proto $scheme;
                  proxy_set_header X-Forwarded-Host $host;
                  proxy_set_header X-Forwarded-Prefix /;
              }
          }
      }
    EOF
  }
}

resource "kubernetes_deployment_v1" "spotify_wrapped" {
  metadata {
    name = "spotify-wrapped-deployment"
    labels = {
      app = "spotify-wrapped"
    }
  }

  spec {
    replicas = 1
    selector {
      match_labels = {
        app = "spotify-wrapped"
      }
    }

    template {
      metadata {
        labels = {
          app = "spotify-wrapped"
        }
      }

      spec {
        automount_service_account_token = false
        container {
          name  = "app"
          image = "europe-central2-docker.pkg.dev/elite-chiller-424111-b6/spotify-wrapped-regestry/spotify-wraped:latest"
          
          port {
            container_port = 5000
          }

          env {
            name  = "FLASK_ENV"
            value = "production"
          }

          volume_mount {
            name       = "secrets-volume"
            mount_path = "/run/secrets"
            read_only  = true
          }

          liveness_probe {
            http_get {
              path = "/health"
              port = 5000
            }
            initial_delay_seconds = 5
            period_seconds        = 10
          }

          security_context {
            allow_privilege_escalation = false
            privileged                 = false
            read_only_root_filesystem  = false
          }
        }

        container {
          name  = "nginx"
          image = "nginx:alpine"

          port {
            container_port = 80
            name           = "http"
          }

          volume_mount {
            name       = "nginx-config-volume"
            mount_path = "/etc/nginx/nginx.conf"
            sub_path   = "nginx.conf"
            read_only  = true
          }
        }

        volume {
          name = "nginx-config-volume"
          config_map {
            name = kubernetes_config_map_v1.nginx_config.metadata[0].name
          }
        }

        volume {
          name = "secrets-volume"
          secret {
            secret_name = kubernetes_secret_v1.spotify_secrets.metadata[0].name
          }
        }

        security_context {
          run_as_non_root = false

          seccomp_profile {
            type = "RuntimeDefault"
          }
        }
      }
    }
  }
}

resource "kubernetes_service_v1" "spotify_wrapped_service" {
  metadata {
    name = "spotify-wrapped-service"
  }

  spec {
    selector = {
      app = kubernetes_deployment_v1.spotify_wrapped.spec[0].template[0].metadata[0].labels.app
    }

    port {
      port        = 80
      target_port = 80
    }

    type = "LoadBalancer"
  }

  depends_on = [time_sleep.wait_service_cleanup]
}

resource "time_sleep" "wait_service_cleanup" {
  depends_on = [google_container_cluster.default]
  destroy_duration = "180s"
}