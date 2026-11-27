// Explicit install-time embedding initialization. HOME is the env model cache.
import { fastembed } from '@mastra/fastembed';
if (typeof fastembed?.doEmbed !== 'function') throw new Error('expected @mastra/fastembed embedding-model doEmbed API; verify pinned SDK export');
await fastembed.doEmbed({values:['memeval install model probe']});
