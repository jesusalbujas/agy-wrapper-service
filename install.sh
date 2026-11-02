#!/usr/bin/env bash
# ==============================================================================
# Instalador universal del comando 'ask' para agy-cli wrapper
# Compatible con Zsh, Bash, Linux y macOS
# ==============================================================================

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ASK_SRC="${SCRIPT_DIR}/bin/ask"

# Colores para la terminal
GREEN='\033[0;32m'
BLUE='\033[0;34m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m'

echo -e "${BLUE}==========================================================${NC}"
echo -e "${BLUE}     Instalador del comando 'ask' (Antigravity CLI API)   ${NC}"
echo -e "${BLUE}==========================================================${NC}"

# 1. Determinar directorio de destino
if [ "$EUID" -eq 0 ]; then
    TARGET_DIR="/usr/local/bin"
else
    TARGET_DIR="${HOME}/.local/bin"
    mkdir -p "$TARGET_DIR"
fi

# 2. Verificar archivo origen
if [ ! -f "$ASK_SRC" ]; then
    echo -e "${RED}[ERROR] No se encontró el archivo bin/ask en ${SCRIPT_DIR}${NC}"
    exit 1
fi

# 3. Instalar ejecutable
echo -e "-> Instalando binario en ${TARGET_DIR}/ask ..."
cp "$ASK_SRC" "${TARGET_DIR}/ask"
chmod +x "${TARGET_DIR}/ask"
echo -e "${GREEN}[OK] Comando 'ask' instalado exitosamente.${NC}"

# 4. Asegurar que TARGET_DIR esté en el PATH
add_to_path() {
    local rc_file="$1"
    if [ -f "$rc_file" ]; then
        if ! grep -q 'PATH=.*\.local/bin' "$rc_file" 2>/dev/null && ! grep -q "$TARGET_DIR" "$rc_file" 2>/dev/null; then
            echo -e "-> Agregando ${TARGET_DIR} al PATH en ${rc_file} ..."
            echo "" >> "$rc_file"
            echo "# Antigravity 'ask' CLI" >> "$rc_file"
            echo "export PATH=\"${TARGET_DIR}:\$PATH\"" >> "$rc_file"
            echo -e "${GREEN}[OK] PATH actualizado en ${rc_file}${NC}"
        fi
    fi
}

if [ "$TARGET_DIR" = "${HOME}/.local/bin" ]; then
    add_to_path "${HOME}/.zshrc"
    add_to_path "${HOME}/.bashrc"
    add_to_path "${HOME}/.profile"
fi

# 5. Instalar Autocompletado con TAB
echo ""
echo -e "-> Configurando autocompletado con la tecla TAB..."

# ZSH Autocompletion
if [ -f "${HOME}/.zshrc" ]; then
    if ! grep -q '_ask_completion' "${HOME}/.zshrc" 2>/dev/null; then
        echo -e "   Configurando autocompletado Zsh en ~/.zshrc ..."
        cat << 'EOF' >> "${HOME}/.zshrc"

# Autocompletado con TAB para el comando ask en Zsh
_ask_completion() {
    local -a models levels
    models=(
        'gemini-3.8-flash:Gemini 3.8 Flash (rápido y recomendado)'
        'gemini-3.7-flash:Gemini 3.7 Flash'
        'gemini-3.6-flash:Gemini 3.6 Flash'
        'gemini-3.1-pro:Gemini 3.1 Pro (razonamiento avanzado)'
        'claude-sonnet-4-6:Claude Sonnet 4.6 (Thinking)'
        'claude-opus-4-6-thinking:Claude Opus 4.6 (Thinking)'
        'gpt-oss-120b-medium:GPT-OSS 120B'
    )
    levels=(
        'low:Bajo esfuerzo de razonamiento (más rápido)'
        'medium:Esfuerzo medio (recomendado)'
        'high:Alto esfuerzo (pensamiento profundo)'
    )

    _arguments -s \
        '(-c --continue)'{-c,--continue}'[Continúa la última conversación (mantiene contexto)]' \
        '(-t --thinking)'{-t,--thinking}'[Muestra el proceso de pensamiento / razonamiento]' \
        '(-m --model)'{-m,--model}'[Modelo a usar]:modelo:(($models))' \
        '(-l --level)'{-l,--level}'[Nivel de razonamiento]:nivel:(($levels))' \
        '(-s --stream)'{-s,--stream}'[Muestra la respuesta en vivo (streaming)]' \
        '(-j --json)'{-j,--json}'[Devuelve la respuesta en formato JSON]' \
        '(-r --raw)'{-r,--raw}'[Muestra texto sin formato Markdown ANSI]' \
        '--models[Lista los modelos disponibles]' \
        '(-h --help)'{-h,--help}'[Muestra la ayuda]' \
        '*:consulta:'
}
compdef _ask_completion ask 2>/dev/null || true
EOF
        echo -e "   Zsh:     ${GREEN}[OK] Autocompletado configurado${NC}"
    else
        echo -e "   Zsh:     ${GREEN}[OK] Ya estaba configurado${NC}"
    fi
fi

# Bash Autocompletion
if [ -f "${HOME}/.bashrc" ]; then
    if ! grep -q '_ask_bash_completion' "${HOME}/.bashrc" 2>/dev/null; then
        echo -e "   Configurando autocompletado Bash en ~/.bashrc ..."
        cat << 'EOF' >> "${HOME}/.bashrc"

# Autocompletado con TAB para el comando ask en Bash
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
        echo -e "   Bash:    ${GREEN}[OK] Autocompletado configurado${NC}"
    else
        echo -e "   Bash:    ${GREEN}[OK] Ya estaba configurado${NC}"
    fi
fi

# 6. Comprobar dependencias
echo ""
echo -e "-> Verificando dependencias del sistema..."
if command -v curl >/dev/null 2>&1; then
    echo -e "   curl:    ${GREEN}[OK]${NC}"
else
    echo -e "   curl:    ${YELLOW}[FALTA] Se recomienda instalar curl ('sudo apt install curl')${NC}"
fi

if command -v jq >/dev/null 2>&1; then
    echo -e "   jq:      ${GREEN}[OK]${NC}"
elif command -v python3 >/dev/null 2>&1; then
    echo -e "   python3: ${GREEN}[OK] (se usará como alternativa a jq)${NC}"
else
    echo -e "   jq:      ${YELLOW}[FALTA] Se recomienda instalar jq ('sudo apt install jq')${NC}"
fi

# 7. Comprobar estado de la API Dockerizada
echo ""
echo -e "-> Comprobando conexión con la API en http://localhost:3000 ..."
if curl -s -f http://localhost:3000/health >/dev/null 2>&1; then
    echo -e "${GREEN}[OK] API Wrapper está activo y respondiendo.${NC}"
else
    echo -e "${YELLOW}[AVISO] La API en http://localhost:3000 no respondió.${NC}"
    echo -e "        Asegúrate de tener el contenedor corriendo con: 'cd docker && docker compose up -d'"
fi

echo ""
echo -e "${GREEN}==========================================================${NC}"
echo -e "${GREEN} => [EXITO] Instalación finalizada con éxito.${NC}"
echo -e "${GREEN}==========================================================${NC}"
echo ""
echo "Ya puedes usar 'ask' desde cualquier terminal (Zsh, Bash, etc.):"
echo ""
echo "  ask dime un chiste de programadores"
echo "  ask -s explica los microservicios en 3 puntos"
echo "  ask -c \"ahora explícame el punto 1 en detalle\""
echo "  ask -m claude-sonnet-4-6 -l high \"¿Cómo funciona una base de datos distribuida?\""
echo "  cat error.log | ask \"Explica este error\""
echo ""
echo "Tip: Presiona la tecla [TAB] tras 'ask -' o 'ask -m ' o 'ask -l ' para ver las sugerencias."
echo ""
