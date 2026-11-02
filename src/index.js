const express = require('express');
const { execFile, exec, spawn } = require('child_process');
const readline = require('readline');
const fs = require('fs');
const path = require('path');
const multer = require('multer');
require('dotenv').config();

const app = express();
const port = process.env.PORT || 3000;

// Ensure agy is available on PATH
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

// File upload configuration (/app/uploads)
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

// Find the Gemini / Antigravity configuration directory
function getGeminiDir() {
  return process.env.GEMINI_DIR || path.join(process.env.HOME || '/root', '.gemini');
}

// Extract thinking entries from a conversation transcript
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

// Extract tool actions from a conversation transcript
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

// Find the agy executable
function getAgyExecutable() {
  const candidate = process.env.AGY_PATH || '/root/.local/bin/agy';
  if (fs.existsSync(candidate)) return candidate;
  return 'agy';
}

// Normalize reasoning effort (effort / level)
function normalizeLevel(level) {
  if (!level) return null;
  const str = String(level).trim().toLowerCase();
  if (['low', '1'].includes(str)) return 'low';
  if (['medium', 'med', '2'].includes(str)) return 'medium';
  if (['high', '3'].includes(str)) return 'high';
  if (['none', 'off', '0'].includes(str)) return null;
  return str;
}

// Resolve model and effort defaults
function resolveModelAndLevel(req) {
  const model = req.body?.model || req.query?.model || process.env.DEFAULT_MODEL || process.env.AGY_MODEL || 'gemini-3.8-flash';
  const defaultEffort = process.env.DEFAULT_LEVEL || process.env.DEFAULT_EFFORT || 'medium';
  const rawLevel = req.body?.level || req.body?.effort || req.query?.level || req.query?.effort;
  let effort = normalizeLevel(rawLevel);

  // Flash and Pro models require --effort unless the model name already includes it
  if (!effort && !model.endsWith('-low') && !model.endsWith('-medium') && !model.endsWith('-high') && !model.startsWith('claude')) {
    effort = normalizeLevel(defaultEffort) || 'medium';
  }

  return { model, effort };
}

// Run agy directly in batch mode (non-streaming)
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

// Use companion-for-agy if the native agy executable is unavailable
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

// Execute with fallback
async function executeAgy(params) {
  try {
    return await runNativeAgy(params);
  } catch (nativeErr) {
    console.warn(`[!] Native agy failed (${nativeErr.message}); trying the companion-for-agy fallback...`);
    return await runCompanionFallback(params);
  }
}

// Run agy in streaming mode (SSE or chunked text)
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
              res.write(`\n💭 [Thoughts]:\n${thinkingText}\n\n`);
            }
          }
        } catch (e) {}
      });
    } catch (e) {}
  }

  // Stop agy if the client closes the HTTP connection prematurely
  res.on('close', () => {
    if (transcriptInterval) clearInterval(transcriptInterval);
    if (!res.writableEnded && !child.killed) {
      console.log('[-] Client disconnected prematurely. Stopping the agy process.');
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

        // Active tools / actions
        if (step.step_type === 'tool' && step.state === 'ACTIVE') {
          const toolName = step.tool_name || step.tool_info?.name || 'tool';
          const cmd = step.tool_info?.parameters?.CommandLine;
          const paramStr = cmd ? ` -> ${cmd}` : '';
          if (isSse) {
            res.write(`data: ${JSON.stringify({ type: 'tool', tool: toolName, params: step.tool_info?.parameters })}\n\n`);
          } else {
            res.write(`\n⚡ [Action: ${toolName}${paramStr}]\n`);
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
      // Ignore unparseable lines and auxiliary plain text
    }
  });

  child.stderr.on('data', (data) => {
    const errText = data.toString();
    // Filter startup notices, if present
    if (!errText.includes('Fetching available models')) {
      console.error(`[agy stderr]: ${errText}`);
    }
  });

  child.on('close', (code) => {
    if (transcriptInterval) clearInterval(transcriptInterval);
    checkTranscript();
    if (code !== 0 && !res.headersSent) {
      res.status(500).send(`agy failed (exit code ${code})`);
    } else {
      res.end();
    }
  });

  child.on('error', (err) => {
    if (transcriptInterval) clearInterval(transcriptInterval);
    console.error(`[-] Failed to start agy: ${err.message}`);
    if (!res.headersSent) {
      res.status(500).json({ error: 'Failed to start agy', details: err.message });
    } else {
      res.end();
    }
  });
}

// Send a plain-text response by default, or JSON when requested
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

  // Include thinking in the text response when requested and available
  if (showThinking && result.thinking) {
    return res.type('text/plain; charset=utf-8').send(`💭 [Thoughts]:\n${result.thinking}\n\n${result.response}`);
  }

  // Return only the response text by default
  res.type('text/plain; charset=utf-8').send(result.response);
}

// ==============================================================================
// 1. GENERAL /ask ENDPOINT (supports stream: true or Accept: text/event-stream)
// ==============================================================================
app.post('/ask', async (req, res) => {
  const query = req.body?.query;

  if (!query) {
    return res.status(400).json({ error: 'The "query" field is required' });
  }

  const { model, effort } = resolveModelAndLevel(req);
  const wantsStream = req.body?.stream === true || req.query?.stream === 'true' || req.headers.accept === 'text/event-stream';
  const continueSession = req.body?.continue === true || req.body?.c === true || req.query?.continue === 'true';
  const conversationId = req.body?.conversation_id || req.body?.conversation || req.query?.conversation_id;

  console.log(`[+] /ask: "${query.substring(0, 80)}..." | Model: ${model} | Effort: ${effort || 'default'} | Stream: ${wantsStream} | Continue: ${continueSession}`);

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
    console.error(`[-] Error in /ask: ${error.message}`);
    res.status(500).json({
      error: 'An error occurred while running the Antigravity CLI.',
      details: error.message
    });
  }
});

// Streaming endpoint for GET and POST requests
app.get('/ask/stream', (req, res) => {
  const query = req.query.query;
  if (!query) return res.status(400).send('The "query" parameter is required');
  const { model, effort } = resolveModelAndLevel(req);
  streamAgy({ query, model, effort, workspaceDir: '/app', res, req });
});

app.post('/ask/stream', (req, res) => {
  const query = req.body?.query;
  if (!query) return res.status(400).send('The "query" field is required');
  const { model, effort } = resolveModelAndLevel(req);
  streamAgy({ query, model, effort, workspaceDir: req.body?.workspaceDir || '/app', res, req });
});

// ==============================================================================
// 2. FILE ANALYSIS ENDPOINT (/ask-file)
//    Supports multipart/form-data uploads or an existing path in /app
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
      return res.status(404).json({ error: `File not found: ${targetFilePath}` });
    }
  } else {
    return res.status(400).json({
      error: 'Attach a file ("file") or specify an existing path ("filePath")'
    });
  }

  const userInstruction = req.body?.query || req.body?.instruction || 'Analyze this file in detail, explain what it does, and highlight potential issues or improvements.';
  const { model, effort } = resolveModelAndLevel(req);

  const fullPrompt = `Instruction: ${userInstruction}\nFile to inspect: "${targetFilePath}" (original name: "${originalName}").\nRead the file and respond to the instruction.`;

  console.log(`[+] /ask-file: File "${originalName}" | Instruction: "${userInstruction.substring(0, 60)}..."`);

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
    console.error(`[-] Error in /ask-file: ${error.message}`);
    res.status(500).json({ error: 'Failed to analyze the file', details: error.message });
  }
});

// ==============================================================================
// 3. UTILITIES AND METADATA (models, health, root)
// ==============================================================================
app.get('/models', (req, res) => {
  const agyBin = getAgyExecutable();
  execFile(agyBin, ['models'], { env: process.env }, (err, stdout, stderr) => {
    if (err) {
      return res.status(500).json({ error: 'Failed to retrieve models', details: stderr });
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
      askFile: 'POST /ask-file (multipart "file" or body: { filePath, query })',
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
