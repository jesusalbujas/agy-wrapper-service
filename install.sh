#!/usr/bin/env bash
# ==============================================================================
# Universal installer for the ask command-line client
# Compatible with Zsh, Bash, Linux, and macOS
# ==============================================================================

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ASK_SRC="${SCRIPT_DIR}/bin/ask"

# Terminal colors
GREEN='\033[0;32m'
BLUE='\033[0;34m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m'

echo -e "${BLUE}==========================================================${NC}"
echo -e "${BLUE}     ask CLI installer (Antigravity API)   ${NC}"
echo -e "${BLUE}==========================================================${NC}"

# 1. Select the installation directory
if [ "$EUID" -eq 0 ]; then
    TARGET_DIR="/usr/local/bin"
else
    TARGET_DIR="${HOME}/.local/bin"
    mkdir -p "$TARGET_DIR"
fi

# 2. Verify the source file
if [ ! -f "$ASK_SRC" ]; then
    echo -e "${RED}[ERROR] Could not find bin/ask in ${SCRIPT_DIR}${NC}"
    exit 1
fi

# 3. Install the executable
echo -e "-> Installing executable to ${TARGET_DIR}/ask ..."
cp "$ASK_SRC" "${TARGET_DIR}/ask"
chmod +x "${TARGET_DIR}/ask"
echo -e "${GREEN}[OK] 'ask' command installed successfully.${NC}"

# 4. Ensure TARGET_DIR is on PATH
add_to_path() {
    local rc_file="$1"
    if [ -f "$rc_file" ]; then
        if ! grep -q 'PATH=.*\.local/bin' "$rc_file" 2>/dev/null && ! grep -q "$TARGET_DIR" "$rc_file" 2>/dev/null; then
            echo -e "-> Adding ${TARGET_DIR} to PATH in ${rc_file} ..."
            echo "" >> "$rc_file"
            echo "# Antigravity 'ask' CLI" >> "$rc_file"
            echo "export PATH=\"${TARGET_DIR}:\$PATH\"" >> "$rc_file"
            echo -e "${GREEN}[OK] PATH updated in ${rc_file}${NC}"
        fi
    fi
}

if [ "$TARGET_DIR" = "${HOME}/.local/bin" ]; then
    add_to_path "${HOME}/.zshrc"
    add_to_path "${HOME}/.bashrc"
    add_to_path "${HOME}/.profile"
fi

# 5. Install TAB completion
echo ""
echo -e "-> Configuring TAB completion..."

# ZSH Autocompletion
if [ -f "${HOME}/.zshrc" ]; then
    if ! grep -q '_ask_completion' "${HOME}/.zshrc" 2>/dev/null; then
        echo -e "   Configuring Zsh completion in ~/.zshrc ..."
        cat << 'EOF' >> "${HOME}/.zshrc"

# TAB completion for the ask command in Zsh
_ask_completion() {
    local -a models levels
    models=(
        'gemini-3.8-flash:Gemini 3.8 Flash (fast and recommended)'
        'gemini-3.7-flash:Gemini 3.7 Flash'
        'gemini-3.6-flash:Gemini 3.6 Flash'
        'gemini-3.1-pro:Gemini 3.1 Pro (advanced reasoning)'
        'claude-sonnet-4-6:Claude Sonnet 4.6 (Thinking)'
        'claude-opus-4-6-thinking:Claude Opus 4.6 (Thinking)'
        'gpt-oss-120b-medium:GPT-OSS 120B'
    )
    levels=(
        'low:Low reasoning effort (faster)'
        'medium:Medium effort (recommended)'
        'high:High effort (deeper reasoning)'
    )

    _arguments -s \
        '(-c --continue)'{-c,--continue}'[Continue the most recent conversation (preserve context)]' \
        '(-t --thinking)'{-t,--thinking}'[Show model thinking]' \
        '(-m --model)'{-m,--model}'[Model to use]:model:(($models))' \
        '(-l --level)'{-l,--level}'[Reasoning effort]:level:(($levels))' \
        '(-s --stream)'{-s,--stream}'[Stream the response as it is generated]' \
        '(-j --json)'{-j,--json}'[Return the response as JSON]' \
        '(-r --raw)'{-r,--raw}'[Show raw text without ANSI Markdown formatting]' \
        '--models[List available models]' \
        '(-h --help)'{-h,--help}'[Show this help]' \
        '*:query:'
}
compdef _ask_completion ask 2>/dev/null || true
EOF
        echo -e "   Zsh:     ${GREEN}[OK] Completion configured${NC}"
    else
        echo -e "   Zsh:     ${GREEN}[OK] Already configured${NC}"
    fi
fi

# Bash Autocompletion
if [ -f "${HOME}/.bashrc" ]; then
    if ! grep -q '_ask_bash_completion' "${HOME}/.bashrc" 2>/dev/null; then
        echo -e "   Configuring Bash completion in ~/.bashrc ..."
        cat << 'EOF' >> "${HOME}/.bashrc"

# TAB completion for the ask command in Bash
_ask_bash_completion() {
    local cur prev opts models levels
    COMPREPLY=()
    cur="${COMP_WORDS[COMP_CWORD]}"
    prev="${COMP_WORDS[COMP_CWORD-1]}"
    opts="-c --continue -t --thinking -m --model -l --level -s --stream -j --json -r --raw -u --url --models -h --help"
    models="gemini-3.8-flash gemini-3.7-flash gemini-3.6-flash gemini-3.1-pro claude-sonnet-4-6 claude-opus-4-6-thinking gpt-oss-120b-medium"
    levels="low medium high"

    case "$prev" in
        -m|--model)
            COMPREPLY=( $(compgen -W "${models}" -- "$cur") )
            return 0
            ;;
        -l|--level)
            COMPREPLY=( $(compgen -W "${levels}" -- "$cur") )
            return 0
            ;;
    esac

    if [[ "$cur" == -* ]]; then
        COMPREPLY=( $(compgen -W "${opts}" -- "$cur") )
        return 0
    fi
}
complete -F _ask_bash_completion ask 2>/dev/null || true
EOF
        echo -e "   Bash:    ${GREEN}[OK] Completion configured${NC}"
    else
        echo -e "   Bash:    ${GREEN}[OK] Already configured${NC}"
    fi
fi

# 6. Check dependencies
echo ""
echo -e "-> Checking system dependencies..."
if command -v curl >/dev/null 2>&1; then
    echo -e "   curl:    ${GREEN}[OK]${NC}"
else
    echo -e "   curl:    ${YELLOW}[MISSING] Install curl if needed ('sudo apt install curl')${NC}"
fi

if command -v jq >/dev/null 2>&1; then
    echo -e "   jq:      ${GREEN}[OK]${NC}"
elif command -v python3 >/dev/null 2>&1; then
    echo -e "   python3: ${GREEN}[OK] (will be used as a fallback for jq)${NC}"
else
    echo -e "   jq:      ${YELLOW}[MISSING] Install jq if needed ('sudo apt install jq')${NC}"
fi

# 7. Check the Dockerized API status
echo ""
echo -e "-> Checking API connectivity at http://localhost:3000 ..."
if curl -s -f http://localhost:3000/health >/dev/null 2>&1; then
    echo -e "${GREEN}[OK] API wrapper is online and responding.${NC}"
else
    echo -e "${YELLOW}[NOTICE] The API at http://localhost:3000 did not respond.${NC}"
    echo -e "        Make sure the container is running with: 'cd docker && docker compose up -d'"
fi

echo ""
echo -e "${GREEN}==========================================================${NC}"
echo -e "${GREEN} => [SUCCESS] Installation completed successfully.${NC}"
echo -e "${GREEN}==========================================================${NC}"
echo ""
echo "You can now use 'ask' from any terminal (Zsh, Bash, etc.):"
echo ""
echo "  ask tell me a programmer joke"
echo "  ask -s explain microservices in three points"
echo "  ask -c \"now explain point 1 in detail\""
echo "  ask -m claude-sonnet-4-6 -l high \"How does a distributed database work?\""
echo "  cat error.log | ask \"Explain this error\""
echo ""
echo "Tip: Press [TAB] after 'ask -', 'ask -m ', or 'ask -l ' to see suggestions."
echo ""
