const express = require('express');
const { execFile, exec, spawn } = require('child_process');
const readline = require('readline');
const fs = require('fs');
const path = require('path');
const multer = require('multer');
require('dotenv').config();

const app = express();
const port = process.env.PORT || 3000;

// Asegurar que las rutas de agy estén en PATH
const homedir = process.env.HOME || '/root';
const extraPaths = [
  path.join(homedir, '.local', 'bin'),
  '/root/.local/bin',
  '/usr/local/bin'
];
for (const p of extraPaths) {
  if (!process.env.PATH.includes(p)) {
    process.env.PATH = `${p}:${process.env.PATH}`;
  }
}

// Configuración para subida de archivos (/app/uploads)
const uploadDir = process.env.UPLOAD_DIR || path.join(__dirname, '../uploads');
if (!fs.existsSync(uploadDir)) {
  fs.mkdirSync(uploadDir, { recursive: true });
}
const storage = multer.diskStorage({
  destination: (req, file, cb) => cb(null, uploadDir),
  filename: (req, file, cb) => {
    const uniqueSuffix = Date.now() + '-' + Math.round(Math.random() * 1e9);
    const safeName = file.originalname.replace(/[^a-zA-Z0-9._-]/g, '_');
    cb(null, `${uniqueSuffix}-${safeName}`);
  }
});
const upload = multer({ storage });

app.use(express.json());
app.use(express.urlencoded({ extended: true }));

// Helper para encontrar el directorio de configuración de Gemini / Antigravity
function getGeminiDir() {
  return process.env.GEMINI_DIR || path.join(process.env.HOME || '/root', '.gemini');
}

// Helper para extraer los pensamientos (Thinking) del transcript de una conversación
function getThinkingFromTranscript(conversationId) {
  if (!conversationId) return null;
  try {
    const transcriptPath = path.join(getGeminiDir(), 'antigravity-cli', 'brain', conversationId, '.system_generated', 'logs', 'transcript.jsonl');
    if (!fs.existsSync(transcriptPath)) return null;

    const content = fs.readFileSync(transcriptPath, 'utf8');
    const lines = content.trim().split('\n');
    const thoughts = [];
    for (const line of lines) {
      if (!line.trim()) continue;
      try {
        const step = JSON.parse(line);
        if (step.thinking && typeof step.thinking === 'string' && step.thinking.trim()) {
          thoughts.push(step.thinking.trim());
        }
      } catch (e) {}
    }
    return thoughts.length > 0 ? thoughts.join('\n\n---\n\n') : null;
  } catch (err) {
    return null;
  }
}

// Helper para extraer las acciones de herramientas ejecutadas
function getToolActionsFromTranscript(conversationId) {
  if (!conversationId) return [];
  try {
    const transcriptPath = path.join(getGeminiDir(), 'antigravity-cli', 'brain', conversationId, '.system_generated', 'logs', 'transcript.jsonl');
    if (!fs.existsSync(transcriptPath)) return [];

    const content = fs.readFileSync(transcriptPath, 'utf8');
    const lines = content.trim().split('\n');
    const tools = [];
    for (const line of lines) {
      if (!line.trim()) continue;
      try {
        const step = JSON.parse(line);
        if (step.tool_calls && Array.isArray(step.tool_calls)) {
          for (const tc of step.tool_calls) {
            tools.push({ name: tc.name, args: tc.args });
          }
        }
      } catch (e) {}
    }
    return tools;
  } catch (err) {
    return [];
  }
}

// Helper para encontrar el binario de agy
function getAgyExecutable() {
  const candidate = process.env.AGY_PATH || '/root/.local/bin/agy';
  if (fs.existsSync(candidate)) return candidate;
  return 'agy';
}

// Normalizar el nivel de razonamiento (effort / level)
function normalizeLevel(level) {
  if (!level) return null;
  const str = String(level).trim().toLowerCase();
  if (['low', 'bajo', '1'].includes(str)) return 'low';
  if (['medium', 'medio', 'med', '2'].includes(str)) return 'medium';
  if (['high', 'alto', '3'].includes(str)) return 'high';
  if (['none', 'off', 'ninguno', '0'].includes(str)) return null;
  return str;
}

// Resolver modelo y nivel con sus valores por defecto
function resolveModelAndLevel(req) {
  const model = req.body?.model || req.query?.model || process.env.DEFAULT_MODEL || process.env.AGY_MODEL || 'gemini-3.8-flash';
  const defaultEffort = process.env.DEFAULT_LEVEL || process.env.DEFAULT_EFFORT || 'medium';
  const rawLevel = req.body?.level || req.body?.effort || req.query?.level || req.query?.effort;
  let effort = normalizeLevel(rawLevel);

  // Modelos tipo flash/pro requieren --effort en la CLI si no está en el nombre
  if (!effort && !model.endsWith('-low') && !model.endsWith('-medium') && !model.endsWith('-high') && !model.startsWith('claude')) {
    effort = normalizeLevel(defaultEffort) || 'medium';
  }

  return { model, effort };
}

// Ejecutar agy directamente en modo batch (no-streaming)
function runNativeAgy({ query, model, effort, workspaceDir, continueSession, conversationId }) {
  return new Promise((resolve, reject) => {
    const agyBin = getAgyExecutable();
    const args = [
      '--dangerously-skip-permissions',
      '--add-dir', workspaceDir || '/app',
      '--output-format', 'json'
    ];

    if (conversationId) {
      args.push('--conversation', conversationId);
    } else if (continueSession) {
      args.push('-c');
    }

    if (model) args.push('--model', model);
    if (effort) args.push('--effort', effort);
    args.push('-p', query);

    execFile(agyBin, args, { env: process.env, maxBuffer: 20 * 1024 * 1024 }, (err, stdout, stderr) => {
      if (err) {
        return reject(new Error(stderr || err.message));
      }

      try {
        const parsed = JSON.parse(stdout.trim());
        const convId = parsed.conversation_id;
        const thinking = convId ? getThinkingFromTranscript(convId) : null;
        const tools = convId ? getToolActionsFromTranscript(convId) : [];
        resolve({
          response: (parsed.response || '').trim(),
          thinking,
          tools,
          meta: parsed
        });
      } catch (e) {
        resolve({
          response: stdout.trim(),
          thinking: null,
          tools: [],
          meta: null
        });
      }
    });
  });
}

// Fallback usando companion-for-agy por si agy nativo no estuviese disponible
function runCompanionFallback({ query, model, effort, workspaceDir }) {
  return new Promise((resolve, reject) => {
    const safeQuery = query.replace(/"/g, '\\"');
    const effortFlag = effort ? `--effort ${effort}` : '';
    const command = `npx companion-for-agy --json --skip-permissions --add-dir ${workspaceDir || '/app'} --model "${model || 'gemini-3.8-flash'}" ${effortFlag} "${safeQuery}"`;

    exec(command, { env: process.env, maxBuffer: 20 * 1024 * 1024 }, (error, stdout, stderr) => {
      if (error) return reject(error);
      try {
        const json = JSON.parse(stdout.trim());
        resolve({
          response: (json.response || '').trim(),
          meta: json
        });
      } catch (e) {
        resolve({
          response: stdout.trim(),
          meta: null
        });
      }
    });
  });
}

// Ejecución con fallback
async function executeAgy(params) {
  try {
    return await runNativeAgy(params);
  } catch (nativeErr) {
    console.warn(`[!] agy nativo falló (${nativeErr.message}), intentando fallback con companion-for-agy...`);
    return await runCompanionFallback(params);
  }
}

// Ejecutar agy en modo STREAMING (SSE o chunked text)
function streamAgy({ query, model, effort, workspaceDir, continueSession, conversationId, res, req }) {
  const agyBin = getAgyExecutable();
  const args = [
    '--dangerously-skip-permissions',
    '--add-dir', workspaceDir || '/app',
    '--output-format', 'stream-json'
  ];

  if (conversationId) {
    args.push('--conversation', conversationId);
  } else if (continueSession) {
    args.push('-c');
  }

  if (model) args.push('--model', model);
  if (effort) args.push('--effort', effort);
  args.push('-p', query);

  const isSse = req.headers.accept === 'text/event-stream' || req.query.sse === 'true' || req.body?.sse === true;

  if (isSse) {
    res.setHeader('Content-Type', 'text/event-stream');
    res.setHeader('Cache-Control', 'no-cache');
    res.setHeader('Connection', 'keep-alive');
  } else {
    res.setHeader('Content-Type', 'text/plain; charset=utf-8');
    res.setHeader('Transfer-Encoding', 'chunked');
  }

  const child = spawn(agyBin, args, { env: process.env });

  let convId = conversationId || null;
  const emittedThinkingIndices = new Set();
  let transcriptInterval = null;

  function checkTranscript() {
    if (!convId) return;
    try {
      const transcriptPath = path.join(getGeminiDir(), 'antigravity-cli', 'brain', convId, '.system_generated', 'logs', 'transcript.jsonl');
      if (!fs.existsSync(transcriptPath)) return;

      const content = fs.readFileSync(transcriptPath, 'utf8');
      const lines = content.trim().split('\n');
      lines.forEach((line, idx) => {
        if (emittedThinkingIndices.has(idx) || !line.trim()) return;
        try {
          const step = JSON.parse(line);
          if (step.thinking && typeof step.thinking === 'string' && step.thinking.trim()) {
            emittedThinkingIndices.add(idx);
            const thinkingText = step.thinking.trim();
            if (isSse) {
              res.write(`data: ${JSON.stringify({ type: 'thinking', text: thinkingText })}\n\n`);
            } else {
              res.write(`\n💭 [Pensamiento]:\n${thinkingText}\n\n`);
            }
          }
        } catch (e) {}
      });
    } catch (e) {}
  }

  // Cancelar proceso de agy si el cliente corta la conexión HTTP prematuramente
  res.on('close', () => {
    if (transcriptInterval) clearInterval(transcriptInterval);
    if (!res.writableEnded && !child.killed) {
      console.log('[-] Cliente desconectado prematuramente. Terminando proceso de agy.');
      child.kill('SIGTERM');
    }
  });

  const rl = readline.createInterface({ input: child.stdout });

  rl.on('line', (line) => {
    try {
      const data = JSON.parse(line.trim());

      if (data.event === 'init' && data.conversation_id) {
        convId = data.conversation_id;
        res.setHeader('X-Conversation-Id', convId);
        if (!transcriptInterval) {
          transcriptInterval = setInterval(checkTranscript, 80);
        }
      } else if (data.event === 'step_update' && data.step_update) {
        const step = data.step_update;

        // Herramientas / Acciones en curso
        if (step.step_type === 'tool' && step.state === 'ACTIVE') {
          const toolName = step.tool_name || step.tool_info?.name || 'herramienta';
          const cmd = step.tool_info?.parameters?.CommandLine;
          const paramStr = cmd ? ` -> ${cmd}` : '';
          if (isSse) {
            res.write(`data: ${JSON.stringify({ type: 'tool', tool: toolName, params: step.tool_info?.parameters })}\n\n`);
          } else {
            res.write(`\n⚡ [Acción: ${toolName}${paramStr}]\n`);
          }
        } else if (step.step_type === 'agent_response' && step.text_delta) {
          const delta = step.text_delta;
          if (isSse) {
            res.write(`data: ${JSON.stringify({ type: 'text', text: delta })}\n\n`);
          } else {
            res.write(delta);
          }
        }
      } else if (data.event === 'result') {
        checkTranscript();
        if (isSse) {
          res.write(`data: [DONE]\n\n`);
        }
      }
    } catch (e) {
      // Línea no parseable o texto plano auxiliar
    }
  });

  child.stderr.on('data', (data) => {
    const errText = data.toString();
    // Filtrar mensajes informativos de inicio si los hubiera
    if (!errText.includes('Fetching available models')) {
      console.error(`[agy stderr]: ${errText}`);
    }
  });

  child.on('close', (code) => {
    if (transcriptInterval) clearInterval(transcriptInterval);
    checkTranscript();
    if (code !== 0 && !res.headersSent) {
      res.status(500).send(`Error en agy (código ${code})`);
    } else {
      res.end();
    }
  });

  child.on('error', (err) => {
    if (transcriptInterval) clearInterval(transcriptInterval);
    console.error(`[-] Error al iniciar agy: ${err.message}`);
    if (!res.headersSent) {
      res.status(500).json({ error: 'Error al iniciar agy', details: err.message });
    } else {
      res.end();
    }
  });
}

// Despachar respuesta (texto puro por defecto, o JSON si fue solicitado)
function formatResponse(req, res, result, model, effort) {
  const convId = result.meta?.conversation_id;
  if (convId) {
    res.setHeader('X-Conversation-Id', convId);
  }

  const showThinking = req.body?.thinking === true || req.query?.thinking === 'true' || process.env.SHOW_THINKING === 'true';
  const wantsJson = req.body?.json === true || req.query?.json === 'true' || (req.headers.accept && req.headers.accept.includes('application/json'));

  if (wantsJson) {
    return res.json({
      response: result.response,
      thinking: result.thinking || null,
      tools: result.tools || [],
      conversation_id: convId
    });
  }

  if (req.body?.full === true || req.query?.full === 'true') {
    return res.json({
      success: true,
      response: result.response,
      thinking: result.thinking || null,
      tools: result.tools || [],
      model,
      level: effort,
      conversation_id: convId,
      meta: result.meta
    });
  }

  // Si se solicitó ver el pensamiento en modo texto y existe
  if (showThinking && result.thinking) {
    return res.type('text/plain; charset=utf-8').send(`💭 [Pensamiento]:\n${result.thinking}\n\n${result.response}`);
  }

  // Por defecto: responder solo el contenido puro del response en texto
  res.type('text/plain; charset=utf-8').send(result.response);
}

// ==============================================================================
// 1. ENDPOINT GENERAL /ask (Soporta streaming con stream: true o Accept: text/event-stream)
// ==============================================================================
app.post('/ask', async (req, res) => {
  const query = req.body?.query;

  if (!query) {
    return res.status(400).json({ error: 'El campo "query" es requerido' });
  }

  const { model, effort } = resolveModelAndLevel(req);
  const wantsStream = req.body?.stream === true || req.query?.stream === 'true' || req.headers.accept === 'text/event-stream';
  const continueSession = req.body?.continue === true || req.body?.c === true || req.query?.continue === 'true';
  const conversationId = req.body?.conversation_id || req.body?.conversation || req.query?.conversation_id;

  console.log(`[+] /ask: "${query.substring(0, 80)}..." | Modelo: ${model} | Nivel: ${effort || 'default'} | Stream: ${wantsStream} | Continue: ${continueSession}`);

  if (wantsStream) {
    return streamAgy({
      query,
      model,
      effort,
      workspaceDir: req.body?.workspaceDir || '/app',
      continueSession,
      conversationId,
      res,
      req
    });
  }

  try {
    const result = await executeAgy({
      query,
      model,
      effort,
      workspaceDir: req.body?.workspaceDir || '/app',
      continueSession,
      conversationId
    });
    formatResponse(req, res, result, model, effort);
  } catch (error) {
    console.error(`[-] Error en /ask: ${error.message}`);
    res.status(500).json({
      error: 'Ocurrió un error al ejecutar la CLI de Antigravity.',
      details: error.message
    });
  }
});

// Endpoint dedicado para streaming GET o POST
app.get('/ask/stream', (req, res) => {
  const query = req.query.query;
  if (!query) return res.status(400).send('Falta el parámetro "query"');
  const { model, effort } = resolveModelAndLevel(req);
  streamAgy({ query, model, effort, workspaceDir: '/app', res, req });
});

app.post('/ask/stream', (req, res) => {
  const query = req.body?.query;
  if (!query) return res.status(400).send('Falta el campo "query"');
  const { model, effort } = resolveModelAndLevel(req);
  streamAgy({ query, model, effort, workspaceDir: req.body?.workspaceDir || '/app', res, req });
});

// ==============================================================================
// 2. ENDPOINT PARA ANÁLISIS DE ARCHIVOS (/ask-file)
//    Soporta subida de archivos (multipart/form-data) o ruta existente en /app
// ==============================================================================
app.post('/ask-file', upload.single('file'), async (req, res) => {
  let targetFilePath = '';
  let originalName = '';

  if (req.file) {
    targetFilePath = req.file.path;
    originalName = req.file.originalname;
  } else if (req.body?.filePath) {
    targetFilePath = path.isAbsolute(req.body.filePath) 
      ? req.body.filePath 
      : path.join('/app', req.body.filePath);
    originalName = path.basename(targetFilePath);

    if (!fs.existsSync(targetFilePath)) {
      return res.status(404).json({ error: `El archivo no existe en: ${targetFilePath}` });
    }
  } else {
    return res.status(400).json({
      error: 'Debes adjuntar un archivo ("file") o especificar una ruta existente ("filePath")'
    });
  }

  const userInstruction = req.body?.query || req.body?.instruction || 'Analiza detalladamente este archivo, explica qué hace y destaca posibles problemas o mejoras.';
  const { model, effort } = resolveModelAndLevel(req);

  const fullPrompt = `Instrucción: ${userInstruction}\nArchivo a inspeccionar: "${targetFilePath}" (Nombre original: "${originalName}").\nPor favor lee el archivo y responde a la instrucción.`;

  console.log(`[+] /ask-file: Archivo "${originalName}" | Instrucción: "${userInstruction.substring(0, 60)}..."`);

  const wantsStream = req.body?.stream === true || req.query?.stream === 'true' || req.headers.accept === 'text/event-stream';
  if (wantsStream) {
    return streamAgy({ query: fullPrompt, model, effort, workspaceDir: '/app', res, req });
  }

  try {
    const result = await executeAgy({
      query: fullPrompt,
      model,
      effort,
      workspaceDir: '/app'
    });
    formatResponse(req, res, result, model, effort);
  } catch (error) {
    console.error(`[-] Error en /ask-file: ${error.message}`);
    res.status(500).json({ error: 'Error al analizar el archivo', details: error.message });
  }
});

// ==============================================================================
// 3. UTILITIES AND METADATA (Modelos, Salud, Raíz)
// ==============================================================================
app.get('/models', (req, res) => {
  const agyBin = getAgyExecutable();
  execFile(agyBin, ['models'], { env: process.env }, (err, stdout, stderr) => {
    if (err) {
      return res.status(500).json({ error: 'Error al obtener modelos', details: stderr });
    }

    const lines = stdout.split('\n')
      .map(l => l.replace(/.*Fetching available models\.\.\./g, '').trim())
      .filter(l => l.length > 0 && !l.toLowerCase().includes('fetching'));

    const parsedModels = lines.map(line => {
      const parts = line.split(/\t+|\s{2,}/);
      return {
        id: parts[0] ? parts[0].trim() : line,
        name: parts[1] ? parts[1].trim() : parts[0]
      };
    });

    res.json({
      levels: ['low', 'medium', 'high'],
      models: parsedModels
    });
  });
});

app.get('/health', (req, res) => {
  res.json({
    status: 'ok',
    defaultModel: process.env.DEFAULT_MODEL || process.env.AGY_MODEL || 'gemini-3.8-flash',
    defaultLevel: process.env.DEFAULT_LEVEL || process.env.DEFAULT_EFFORT || 'medium',
    workspace: '/app',
    uploadsDir: uploadDir
  });
});

app.get('/', (req, res) => {
  res.json({
    status: 'running',
    service: 'agy-cli-wrapper',
    endpoints: {
      ask: 'POST /ask (body: { query, model?, level?, stream?, json? })',
      stream: 'GET|POST /ask/stream (query/body: { query, model?, level? })',
      askFile: 'POST /ask-file (multipart "file" o body: { filePath, query })',
      models: 'GET /models',
      health: 'GET /health'
    }
  });
});

app.listen(port, () => {
  console.log(`Antigravity CLI wrapper listening on port ${port}`);
  console.log(`Default model: ${process.env.DEFAULT_MODEL || process.env.AGY_MODEL || 'gemini-3.8-flash'}`);
  console.log(`Uploads directory: ${uploadDir}`);
  console.log('API ready to receive requests.');
});
