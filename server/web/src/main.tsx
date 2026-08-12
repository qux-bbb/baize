import React from 'react'
import ReactDOM from 'react-dom/client'
import { AgGridProvider } from 'ag-grid-react'
import { AllCommunityModule } from 'ag-grid-community'
import App from './App'
import './index.css'

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <AgGridProvider modules={[AllCommunityModule]}>
      <App />
    </AgGridProvider>
  </React.StrictMode>,
)
