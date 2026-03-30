#!/usr/bin/env bash
# ==========================================================
# Model Armor Batch Processor
# ----------------------------------------------------------
# - Reads prompts (paragraph mode)
# - Sends to Model Armor sanitizeUserPrompt endpoint
# - Outputs JSONL results
# ==========================================================

set -euo pipefail

# ------------------ Argument Validation ------------------
if [ "$#" -ne 2 ]; then
  echo "Usage: $0 <input_file> <output_jsonl>"
  exit 2
fi

INPUT_FILE="$1"
OUTPUT_FILE="$2"

# ------------------ Configuration ------------------

# Get active GCP project
PROJECT="$(gcloud config get-value project 2>/dev/null)"
if [ -z "$PROJECT" ]; then
  echo "ERROR: No active GCP project. Run: gcloud config set project <PROJECT_ID>"
  exit 2
fi

LOCATION="europe-west4"

# Require Model Armor template from env
: "${MODEL_ARMOR_TEMPLATE:?ERROR: MODEL_ARMOR_TEMPLATE environment variable not set}"
TEMPLATE="$MODEL_ARMOR_TEMPLATE"

MODEL_ARMOR_HOST="modelarmor.${LOCATION}.rep.googleapis.com"

# ------------------ Validate Template ------------------

echo "Validating Model Armor template: $TEMPLATE"

if ! gcloud beta model-armor templates describe "$TEMPLATE" \
    --project "$PROJECT" \
    --location "$LOCATION" >/dev/null 2>&1; then
  echo "ERROR: Model Armor template '$TEMPLATE' not found in project '$PROJECT' (location: $LOCATION)"
  exit 2
fi

echo "Template validation successful"

# ------------------ Temp Files ------------------

TMP_DIR=$(mktemp -d)
RESP_PRETTY="${TMP_DIR}/ma_response_pretty.json"
RESP_REDACTED="${TMP_DIR}/ma_response_redacted.json"

# ------------------ Redaction ------------------

redact_for_log() {
  sed -E \
    -e 's/[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,6}/[REDACTED_EMAIL]/g' \
    -e 's/\+?[0-9][0-9 \-]{6,}/[REDACTED_PHONE]/g' \
    -e 's/[A-Z]{2}[0-9A-Z]{13,34}/[REDACTED_IBAN]/g'
}

# ------------------ Logging ------------------

log() {
  printf '%s %s\n' "$(date --utc +"%Y-%m-%dT%H:%M:%SZ")" "$*"
}

# ------------------ Auth Check ------------------

if ! gcloud auth print-access-token >/dev/null 2>&1; then
  log "ERROR: gcloud not authenticated. Run: gcloud auth login"
  exit 2
fi

ACCESS_TOKEN="$(gcloud auth print-access-token)"

# ------------------ Output File ------------------

touch "$OUTPUT_FILE"

# ==========================================================
# Main Processing Loop
# ==========================================================

awk -v RS="" -v ORS="\0" '{gsub(/\r/,""); print $0 RS}' "$INPUT_FILE" | \
while IFS= read -r -d '' record || [ -n "$record" ]; do

  # Trim whitespace
  text="$(printf '%s' "$record" | sed -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//')"

  # Skip empty records
  if [ -z "$text" ]; then
    continue
  fi

  # ------------------ Build Payload ------------------
  payload=$(jq -nc --arg t "$text" '{userPromptData: {text: $t}}')

  # ------------------ Log Preview ------------------
  echo "$payload" | redact_for_log | sed 's/^.\{200\}$/.../' >/dev/stderr

  # ------------------ API Call ------------------
  resp=$(curl -sS -X POST \
    -H "Authorization: Bearer ${ACCESS_TOKEN}" \
    -H "Content-Type: application/json; charset=utf-8" \
    --data-binary "$payload" \
    "https://${MODEL_ARMOR_HOST}/v1/projects/${PROJECT}/locations/${LOCATION}/templates/${TEMPLATE}:sanitizeUserPrompt" \
    || true)

  # Rate limiting (2 RPS)
  sleep 0.5

  # ------------------ Normalize Response ------------------
  if echo "$resp" | jq . >/dev/null 2>&1; then
    echo "$resp" | jq . > "$RESP_PRETTY"
  else
    jq -n --arg raw "$resp" '{raw_response: $raw}' > "$RESP_PRETTY"
  fi

  # ------------------ Redact Response ------------------
  cat "$RESP_PRETTY" | redact_for_log > "$RESP_REDACTED"

  # ------------------ Extract Fields ------------------
  was_sanitized=$(jq -r '(.sanitizedPrompt.wasSanitized // .wasSanitized // false) | tostring' "$RESP_PRETTY" 2>/dev/null || echo "false")
  policy_matches=$(jq -c '.policyMatches // []' "$RESP_PRETTY" 2>/dev/null || echo "[]")

  # ------------------ Build Result Object ------------------
  result=$(jq -n \
    --arg ts "$(date --utc +"%Y-%m-%dT%H:%M:%SZ")" \
    --arg project "$PROJECT" \
    --arg location "$LOCATION" \
    --arg template "$TEMPLATE" \
    --arg raw_preview "$(printf '%s' "$text" | redact_for_log | sed 's/^.\{200\}$/.../')" \
    --argjson was_sanitized "$was_sanitized" \
    --argjson policy_matches "$policy_matches" \
    --slurpfile resp_red "$RESP_REDACTED" \
    '{
      timestamp: $ts,
      project: $project,
      location: $location,
      template: $template,
      payload_preview: $raw_preview,
      was_sanitized: $was_sanitized,
      policy_matches: $policy_matches,
      response: $resp_red[0]
    }')

  # ------------------ Append Output ------------------
  echo "$result" >> "$OUTPUT_FILE"

  # ------------------ Debug Output ------------------
  debug_file="${TMP_DIR}/debug_$(date +%s%N).json"
  echo "$resp" > "$debug_file"

  log "Processed record; appended to ${OUTPUT_FILE}; debug file: ${debug_file}"

done

log "All records processed. Results appended to ${OUTPUT_FILE}"