// Forwards the API and its websocket to a locally running daemon (`make run` in worker/control/cmd, which listens on 8080).
const target = process.env['CONTROL_TARGET'] ?? 'http://localhost:8080';

export default {
  '/api': {
    target,
    secure: false, // the daemon uses a self-signed certificate by default
    ws: true,
    changeOrigin: true,
    configure: (proxy) => {
      // The daemon only accepts websockets from its own origin.
      proxy.on('proxyReqWs', (proxyReq) => proxyReq.setHeader('origin', target));
    },
  },
};
