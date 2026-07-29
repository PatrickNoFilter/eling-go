#!/usr/bin/env bash
# ELING OSINT Person Search (adapted from Hermes Skills Bundle)
# Cross-platform person verification across 12+ social/public platforms
TARGET="${1:-}"
if [ -z "$TARGET" ]; then
  echo '{"error":"Target name/username required. Usage: osint-person-search <name|username>"}'
  exit 1
fi
SESSION="osint-$(date +%s)"
echo "{\"session\":\"${SESSION}\",\"target\":\"${TARGET}\"}"
cat <<EOF
## OSINT Person Search Protocol

### Target: ${TARGET}

### Platforms to Check (open in parallel tabs)
1. **GitHub**: "https://github.com/${TARGET}" and search
2. **Twitter/X**: "https://x.com/${TARGET}"
3. **LinkedIn**: Search for "${TARGET}"
4. **Reddit**: "https://reddit.com/user/${TARGET}"
5. **Stack Overflow**: "https://stackoverflow.com/users?q=${TARGET}"
6. **Medium**: "https://medium.com/@${TARGET}"
7. **Dev.to**: "https://dev.to/${TARGET}"
8. **Keybase**: "https://keybase.io/${TARGET}"
9. **AngelList/Wellfound**: Search
10. **Crunchbase**: "https://crunchbase.com/person/${TARGET}"

### Cross-Referencing
- Compare profile pictures, bios, and links across platforms
- Look for verified badges, org affiliations
- Check GitHub pinned repos for personal projects
- Note follower counts, account creation dates

### Rules
- Public information only
- Don't log in to any service
- Record each profile found (even empty/not found)
- Flag inconsistencies between profiles
EOF
