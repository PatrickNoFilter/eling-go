#!/usr/bin/env bash
# ELING Job Search Automation (adapted from Hermes Skills Bundle)
# Scours LinkedIn, Indeed, Glassdoor for job roles matching criteria
JOB_QUERY="${1:-}"
LOCATION="${2:-remote}"
if [ -z "$JOB_QUERY" ]; then
  echo '{"error":"Job query required. Usage: job-search-automation <query> [location]"}'
  exit 1
fi
SESSION="job-search-$(date +%s)"
echo "{\"session\":\"${SESSION}\",\"query\":\"${JOB_QUERY}\",\"location\":\"${LOCATION}\"}"
cat <<EOF
## Job Search Automation Protocol

### Session: ${SESSION}
### Query: ${JOB_QUERY}
### Location: ${LOCATION}

### Methodology
1. **LinkedIn**: Search "https://www.linkedin.com/jobs/search/?keywords=$(echo ${JOB_QUERY} | sed 's/ /+/g')&location=${LOCATION}"
2. **Indeed**: "https://www.indeed.com/q-$(echo ${JOB_QUERY} | sed 's/ /+/g')-l-${LOCATION}.html"
3. **Glassdoor**: "https://www.glassdoor.com/Job/jobs.htm?sc.keyword=$(echo ${JOB_QUERY} | sed 's/ /+/g')&locT=C&locId=0"

For each source:
- Take snapshot to read listings
- Extract: job title, company, location, posting date, salary range
- Click through to top 3-5 jobs for details
- Track which are "posted today/this week"

### Output: Structured table of findings
EOF
