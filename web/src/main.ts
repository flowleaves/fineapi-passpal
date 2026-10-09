import { createApp } from "vue";
import { createRouter, createWebHistory } from "vue-router";
import App from "./App.vue";
import { routes } from "./router";
import { useAuth } from "./composables/useAuth";
import "./styles/tokens.css";

const router = createRouter({
  history: createWebHistory(),
  routes,
});

// 路由守卫：未登录访问受保护页面跳登录；已登录访问登录页跳工作台。
router.beforeEach(async (to) => {
  const { ready, refresh, authenticated } = useAuth();
  if (!ready.value) {
    await refresh();
  }
  if (to.meta.auth && !authenticated.value) {
    return { name: "login", query: to.fullPath === "/app" ? undefined : { redirect: to.fullPath } };
  }
  if (to.name === "login" && authenticated.value) {
    return { name: "workspace" };
  }
  return true;
});

createApp(App).use(router).mount("#app");
