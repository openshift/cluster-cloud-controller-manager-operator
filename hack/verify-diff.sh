FILE_DIFF=$(git ls-files -o --exclude-standard)

if [ "$FILE_DIFF" != "" ]; then
  echo "Found untracked files:"
  echo $FILE_DIFF
fi

if [ "$OPENSHIFT_CI" == "true" ]; then
  if [ "$FILE_DIFF" != "" ]; then
    exit 1
  fi
  git diff --exit-code
fi
