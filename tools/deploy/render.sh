#!/bin/bash
# Render deploy/k3s/<file> for the production namespace (cliproxy-google).
# Single rule: namespace + in-cluster DNS. Do not add a second rule (double-substitution bug).
sed -E 's/(namespace: )cliproxy$/\1cliproxy-google/; s/\.cliproxy(\.svc|,)/.cliproxy-google\1/g' "$1"
