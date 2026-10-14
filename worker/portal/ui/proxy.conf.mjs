// Forwards the API and its websocket to a locally running portal (`make run` in worker/portal/cmd, 8443 with TLS).
// The portal serves its own services (/api/v1/portal) and relays the daemon API after
// checking the session, so the dev server talks to it and not to the daemon. Set its password first:
//   control-portal password
const target = process.env['PORTAL_TARGET'] ?? 'https://localhost:8443';

export default {
  '/api': {
    target,
    secure: false, // the portal uses a self-signed certificate by default
    ws: true,
    changeOrigin: true,
    configure: (proxy) => {
      // The portal only accepts requests and websockets from its own origin.
      proxy.on('proxyReq', (proxyReq) => proxyReq.setHeader('origin', target));
      proxy.on('proxyReqWs', (proxyReq) => proxyReq.setHeader('origin', target));
    },
  },
};
