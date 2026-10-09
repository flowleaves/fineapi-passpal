import type { RouteRecordRaw } from "vue-router";

// 只有 3 个路由：登录、工作台、设置。
// 详情与导入都在 Drawer / Modal 内完成，不占用路由。
export const routes: RouteRecordRaw[] = [
  { path: "/", redirect: "/app" },
  {
    path: "/login",
    name: "login",
    component: () => import("@/pages/LoginPage.vue"),
  },
  {
    path: "/app",
    name: "workspace",
    component: () => import("@/pages/WorkspacePage.vue"),
    meta: { auth: true },
  },
  {
    path: "/settings",
    name: "settings",
    component: () => import("@/pages/SettingsPage.vue"),
    meta: { auth: true },
  },
  { path: "/:pathMatch(.*)*", redirect: "/app" },
];
