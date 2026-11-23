#!/usr/bin/env bash
set -euo pipefail

: "${S3_ENDPOINT:?S3_ENDPOINT is required}"
: "${S3_BUCKET:?S3_BUCKET is required}"
: "${S3_ACCESS_KEY_ID:?S3_ACCESS_KEY_ID is required}"
: "${S3_SECRET_ACCESS_KEY:?S3_SECRET_ACCESS_KEY is required}"
: "${ALLOWED_ORIGINS:?ALLOWED_ORIGINS is required}"

config=$(jq -n --argjson origins "$ALLOWED_ORIGINS" '{
  CORSRules: [{
    AllowedOrigins: $origins,
    AllowedMethods: ["GET", "HEAD", "PUT"],
    AllowedHeaders: ["Content-Type"],
    ExposeHeaders: ["ETag"],
    MaxAgeSeconds: 3600
  }]
}')

if [ "${DRY_RUN:-0}" = "1" ]; then
  printf '%s\n' "$config"
  exit 0
fi

AWS_ACCESS_KEY_ID="$S3_ACCESS_KEY_ID" \
AWS_SECRET_ACCESS_KEY="$S3_SECRET_ACCESS_KEY" \
AWS_DEFAULT_REGION="${S3_REGION:-auto}" \
  aws s3api put-bucket-cors \
    --endpoint-url "$S3_ENDPOINT" \
    --bucket "$S3_BUCKET" \
    --cors-configuration "$config"
