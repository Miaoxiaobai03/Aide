'use client';
import { useState } from 'react';
import { Sidebar, type ViewType } from '@/components/Sidebar';
import { Header } from '@/components/Header';
import { InterviewView, type InterviewResumeRequest } from '@/components/InterviewView';
import { ResumeView } from '@/components/ResumeView';
import { MatchView } from '@/components/MatchView';
import { DashboardView } from '@/components/DashboardView';
import { TrainingHistoryView } from '@/components/TrainingHistoryView';
import { ConfigModal } from '@/components/ConfigModal';
import { CoachChat } from '@/components/CoachChat';

export default function Home() {
  const [model, setModel] = useState('');
  const [activeView, setActiveView] = useState<ViewType>('chat');
  const [resumeInterview, setResumeInterview] = useState<InterviewResumeRequest | null>(null);
  const [mobileMenuOpen, setMobileMenuOpen] = useState(false);
  const [configOpen, setConfigOpen] = useState(false);
  const [chatState, setChatState] = useState({ id: '', questions: 0 });
  const [resetKey, setResetKey] = useState(0);
  const [openChatId, setOpenChatId] = useState<string | undefined>();
  return <div className="flex h-screen">
    {mobileMenuOpen && <div className="fixed inset-0 z-40 md:hidden">
      <div className="absolute inset-0 bg-black/20 backdrop-blur-sm" onClick={() => setMobileMenuOpen(false)} />
      <div className="absolute left-0 top-0 h-full w-72">
        <Sidebar activeView={activeView} onViewChange={v => { setActiveView(v); setMobileMenuOpen(false); }} model={model} onModelChange={setModel} onOpenConfig={() => { setMobileMenuOpen(false); setConfigOpen(true); }} />
      </div>
    </div>}
    <Sidebar activeView={activeView} onViewChange={setActiveView} model={model} onModelChange={setModel} onOpenConfig={() => setConfigOpen(true)} />
    <main className="flex flex-1 flex-col min-w-0">
      <Header activeView={activeView} sessionId={chatState.id || null} questionsCount={chatState.questions} onReset={() => { setOpenChatId(undefined); setResetKey(v => v+1); }} onMobileMenu={() => setMobileMenuOpen(true)} />
      {activeView === 'chat' && <CoachChat model={model} resetKey={resetKey} openId={openChatId} onState={(id, questions) => setChatState({id,questions})} />}
      {activeView === 'interview' && <InterviewView resumeRequest={resumeInterview} onResumeConsumed={() => setResumeInterview(null)} />}
      {activeView === 'resume' && <ResumeView />}
      {activeView === 'match' && <MatchView />}
      {activeView === 'dashboard' && <DashboardView onOpenPractice={id => { setOpenChatId(id);setActiveView('chat'); }} />}
      {activeView === 'history' && <TrainingHistoryView onResumeInterview={request => { setResumeInterview(request); setActiveView('interview'); }} onOpenPractice={id => { setOpenChatId(id);setActiveView('chat'); }} />}
    </main>
    <ConfigModal open={configOpen} onClose={() => setConfigOpen(false)} onSaved={() => {}} />
  </div>;
}
