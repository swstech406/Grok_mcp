import { changePassword, revokeSessions } from "../api.js";
import { showToast } from "../components/toast.js";
import { createFormDataObject } from "../utils.js";
import { getErrorMessage, withRetryAfter } from "./event-helpers.js";

export function createAccountEvents({ state, renderApplication, handleSessionError }) {
  async function submitPasswordChange(formElement) {
    const formData = createFormDataObject(formElement);
    const currentPassword = String(formData.current_password || "");
    const newPassword = String(formData.new_password || "");
    const confirmedPassword = String(formData.confirm_new_password || "");
    if (newPassword !== confirmedPassword) {
      showToast("无法修改密码", "两次输入的新密码不一致。", "error");
      return;
    }

    state.formBusy = true;
    renderApplication();
    try {
      const replacementSession = await changePassword({
        current_password: currentPassword,
        new_password: newPassword
      });
      state.user = replacementSession.user;
      showToast("密码已更新", "旧会话已全部失效，当前标签页已切换到新会话。", "success");
    } catch (error) {
      if (!handleSessionError(error)) {
        showToast("无法修改密码", withRetryAfter(getErrorMessage(error), error), "error");
      }
    } finally {
      state.formBusy = false;
      if (state.authenticated) {
        renderApplication();
      }
    }
  }

  async function submitSessionRevocation() {
    state.formBusy = true;
    renderApplication();
    try {
      const replacementSession = await revokeSessions();
      state.user = replacementSession.user;
      showToast("会话已吊销", "此前签发的所有面板会话均已失效。", "success");
    } catch (error) {
      if (!handleSessionError(error)) {
        showToast("无法吊销会话", withRetryAfter(getErrorMessage(error), error), "error");
      }
    } finally {
      state.formBusy = false;
      if (state.authenticated) {
        renderApplication();
      }
    }
  }

  return { submitPasswordChange, submitSessionRevocation };
}
