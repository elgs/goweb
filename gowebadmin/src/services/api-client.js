import env from '../env.js';

class APIClient {
   constructor(baesUrl, sendToken = false, defaultHeaders = {}) {
      this.baesUrl = baesUrl;
      this.sendToken = sendToken;
      this.defaultHeaders = defaultHeaders;
   }

   async _fetch(method, url = '', data = {}, headers = {}) {
      if (!url.toLowerCase().startsWith('https://') && !url.toLowerCase().startsWith('http://')) {
         url = this.baesUrl + url;
      }

      if (method === 'GET' && data && typeof data === 'object') {
         // encode data and append to url
         const queryString = paramsToQueryString(data);
         data = null;
         if (url.endsWith('?')) {
            url += queryString;
         } else if (url.indexOf('?') >= 0) {
            url += ('&' + queryString);
         } else {
            url += ('?' + queryString);
         }
      }

      if (this.sendToken) {
         const token = sessionStorage.getItem('access_token') ?? localStorage.getItem('access_token');
         if (token) {
            headers['authorization'] = token;
         }
      }
      const response = await fetch(url, {
         method,
         headers: { ...this.defaultHeaders, ...headers },
         body: data ? JSON.stringify(data) : null,
      });

      if (response.status === 401) {
         leanweb?.onAuthFailed();
      } else if (response.status === 200) {
         if (leanweb.urlHashPath.startsWith('#/login')) {
            leanweb.urlHashPath = '#/';
            leanweb.urlHashParams = {};
         }
      }

      return response;
   }

   post(url, data, headers) { return this._fetch('POST', url, data, headers); }
   get(url, data, headers) { return this._fetch('GET', url, data, headers); }
   patch(url, data, headers) { return this._fetch('PATCH', url, data, headers); }
   delete(url, data, headers) { return this._fetch('DELETE', url, data, headers); }
   put(url, data, headers) { return this._fetch('PUT', url, data, headers); }
   options(url, data, headers) { return this._fetch('OPTIONS', url, data, headers); }
}

const paramsToQueryString = (params) => {
   return Object.keys(params).map(k => {
      const v = params[k];
      if (Array.isArray(v)) {
         return v.reduce((vacc, vcurr) => {
            return `${vacc}${k}=${encodeURIComponent(vcurr)}&`;
         }, '');
      } else {
         return `${k}=${encodeURIComponent(v)}&`;
      }
   }).reduce((acc, curr) => acc + curr, '').slice(0, -1);
};

export const api = new APIClient(env.apiUrl, true);
